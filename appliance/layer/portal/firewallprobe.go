// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MeasurementFile is where the host firewall's owner publishes what it measured after it
// loaded a policy: root-owned, mode 0644, one document per load.
const MeasurementFile = "/run/olivares-firewall/measured.json"

// bootIDFile is where the kernel reports this boot's identity.
const bootIDFile = "/proc/sys/kernel/random/boot_id"

// consolePort is the console's row in the firewall policy.
const consolePort = "9443/tcp"

const (
	measurementSchema   = "olivares-firewall-measurement/v1"
	maxMeasurementBytes = 64 * 1024
	maxMeasuredRows     = 64
	// everyInterface is the interface list of a row that admits its port on every interface.
	everyInterface = "*"
)

var (
	bootIDPattern       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	policyDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	portPattern         = regexp.MustCompile(`^([0-9]{1,5})/(tcp|udp)$`)
)

// MeasuredFirewall is the console's firewall probe: it reads the measurement the firewall's
// owner published and decides whether the prerequisite for remote exposure holds for
// Selection. It holds when the measurement is this boot's, the input policy drops what no row
// admits, and the rows admit 9443/tcp on exactly the selected management interfaces: not on
// another interface, not on every interface, and not on only some of the selected ones.
//
// The file is read only when it is a regular file, not a symbolic link, owned by root and
// writable by neither its group nor others. An absent, foreign-owned, malformed or stale
// measurement, or a boot identity that cannot be read, is unmeasured, never a guess, so the
// console stays on loopback until the owner publishes one. The probe only reads.
type MeasuredFirewall struct {
	// Path is the measurement, MeasurementFile on an installed console.
	Path string
	// BootID is the kernel's report of this boot's identity.
	BootID string
	// Selection is the operator's selection the rows are judged against.
	Selection Selection
}

// measurementDocument is the measurement's closed schema; every field is required.
type measurementDocument struct {
	SchemaVersion string      `json:"schema_version"`
	BootID        string      `json:"boot_id"`
	MeasuredAt    string      `json:"measured_at"`
	PolicyDigest  string      `json:"policy_digest"`
	InputPolicy   string      `json:"input_policy"`
	Rows          []policyRow `json:"rows"`
}

// policyRow is one row of the loaded policy: a port it admits and where.
type policyRow struct {
	Port       string   `json:"port"`
	Interfaces []string `json:"interfaces"`
}

// MeasureFirewall reads the published measurement.
func (p MeasuredFirewall) MeasureFirewall() FirewallMeasurement {
	data, present, reason := readRootOwned(p.Path, maxMeasurementBytes)
	if !present || reason != "" {
		return FirewallMeasurement{}
	}
	boot, ok := readBootID(p.BootID)
	if !ok {
		return FirewallMeasurement{}
	}
	document, ok := parseMeasurement(data)
	if !ok || document.BootID != boot {
		return FirewallMeasurement{}
	}
	admitted, everywhere := consoleRow(document.Rows)
	selected := slices.Clone(p.Selection.ManagementInterfaces)
	slices.Sort(selected)
	holds := document.InputPolicy == "drop" && !everywhere && len(selected) > 0 && slices.Equal(admitted, selected)
	return FirewallMeasurement{Holds: holds, Policy: Measured(policyFact(document, admitted, everywhere))}
}

// readBootID reads this boot's identity; false when it cannot be read or is not one.
func readBootID(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	return id, bootIDPattern.MatchString(id)
}

// parseMeasurement decodes and validates one measurement document; false when it is not one.
func parseMeasurement(data []byte) (measurementDocument, bool) {
	if oneDocumentWithoutNull(data) != "" {
		return measurementDocument{}, false
	}
	var document measurementDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return measurementDocument{}, false
	}
	if _, err := time.Parse(time.RFC3339, document.MeasuredAt); err != nil {
		return measurementDocument{}, false
	}
	switch {
	case document.SchemaVersion != measurementSchema,
		!bootIDPattern.MatchString(document.BootID),
		!policyDigestPattern.MatchString(document.PolicyDigest),
		document.InputPolicy != "drop" && document.InputPolicy != "accept",
		document.Rows == nil,
		len(document.Rows) > maxMeasuredRows:
		return measurementDocument{}, false
	}
	for _, row := range document.Rows {
		if !validPort(row.Port) || !validRowInterfaces(row.Interfaces) {
			return measurementDocument{}, false
		}
	}
	return document, true
}

// validPort accepts "<1-65535>/tcp" and "<1-65535>/udp".
func validPort(port string) bool {
	match := portPattern.FindStringSubmatch(port)
	if match == nil {
		return false
	}
	n, err := strconv.Atoi(match[1])
	return err == nil && n >= 1 && n <= 65535
}

// validRowInterfaces accepts "*" alone, or one to sixteen distinct interface names.
func validRowInterfaces(names []string) bool {
	if len(names) == 1 && names[0] == everyInterface {
		return true
	}
	if len(names) < 1 || len(names) > maxManagementInterfaces {
		return false
	}
	for _, name := range names {
		if !interfaceName(name) {
			return false
		}
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	return len(slices.Compact(sorted)) == len(names)
}

// consoleRow returns the interfaces the rows admit the console's port on, sorted and distinct,
// and whether any row admits it on every interface.
func consoleRow(rows []policyRow) (interfaces []string, everywhere bool) {
	for _, row := range rows {
		if row.Port != consolePort {
			continue
		}
		for _, name := range row.Interfaces {
			if name == everyInterface {
				everywhere = true
				continue
			}
			interfaces = append(interfaces, name)
		}
	}
	slices.Sort(interfaces)
	return slices.Compact(interfaces), everywhere
}

// policyFact summarizes the measured policy for a caller on this host.
func policyFact(document measurementDocument, admitted []string, everywhere bool) string {
	where := "no " + consolePort + " row"
	switch {
	case everywhere:
		where = consolePort + " on every interface"
	case len(admitted) > 0:
		where = consolePort + " on " + strings.Join(admitted, ", ")
	}
	digest := strings.TrimPrefix(document.PolicyDigest, "sha256:")
	return "input " + document.InputPolicy + "; " + where + "; policy sha256:" + digest[:12]
}
