// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// subjectDockerHost is the FindingReport SubjectKind for a Docker daemon that is
// present (socket on disk) but cannot be queried.
const subjectDockerHost = "docker.host"

// dockerBaseURL is the host part of every request; the unix socket is reached via
// the custom DialContext, so the host name is a placeholder.
const dockerBaseURL = "http://docker"

// dockerContainer is the minimal-data subset of a /containers/json entry. Env and
// the full Labels map are deliberately NOT decoded, so they can never be emitted.
type dockerContainer struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
}

// dockerInfo is the minimal subset of /info used to name the host.
type dockerInfo struct {
	Name string `json:"Name"`
}

// gatherDocker discovers containers and their images from the Docker daemon over
// its unix socket, using read-only GET calls only. If the socket path is absent
// on disk the daemon is simply not present here: SKIP silently (no finding). If
// the socket exists but the API errors, that is a configured-but-failing target:
// emit exactly one health finding and return.
//
// It issues GET /version, /info, /containers/json?all=1, /images/json and
// /networks. /version and /networks are queried to confirm reachability and
// surface the network topology dimension; only containers/images become edges in
// this version (entities are named by edge refs, and a container's image is its
// natural inventory link). Container Env and Labels are never read or emitted.
func gatherDocker(ctx context.Context, cfg config, sink sdk.Sink, at time.Time) error {
	if !socketExists(cfg.dockerSocket) {
		return nil // daemon not present here — skip silently, not a finding
	}

	client := dockerClient(cfg.dockerSocket, cfg.timeout)

	// Confirm reachability first; a dead/again-errored daemon yields one finding.
	if _, err := dockerGET(ctx, client, "/version"); err != nil {
		return sink.Emit(ctx, healthFinding(subjectDockerHost, cfg.host, "docker daemon unreachable", err, at))
	}

	hostName := cfg.host
	if info, err := dockerGET(ctx, client, "/info"); err == nil {
		var di dockerInfo
		if json.Unmarshal(info, &di) == nil {
			// The daemon-reported name becomes an emitted OriginRef, so it passes
			// through redact.Clean for the same minimal-data guarantee as every
			// other ref; an empty result leaves the configured host in place.
			if name := redact.Clean(strings.TrimSpace(di.Name)); name != "" {
				hostName = name
			}
		}
	}

	// Read-only topology probes: surface failures as a finding, do not fabricate.
	if _, err := dockerGET(ctx, client, "/images/json"); err != nil {
		return sink.Emit(ctx, healthFinding(subjectDockerHost, hostName, "docker images list failed", err, at))
	}
	if _, err := dockerGET(ctx, client, "/networks"); err != nil {
		return sink.Emit(ctx, healthFinding(subjectDockerHost, hostName, "docker networks list failed", err, at))
	}

	body, err := dockerGET(ctx, client, "/containers/json?all=1")
	if err != nil {
		return sink.Emit(ctx, healthFinding(subjectDockerHost, hostName, "docker containers list failed", err, at))
	}
	var containers []dockerContainer
	if err := json.Unmarshal(body, &containers); err != nil {
		return sink.Emit(ctx, healthFinding(subjectDockerHost, hostName, "docker containers decode failed", err, at))
	}

	sort.Slice(containers, func(i, j int) bool { return containers[i].ID < containers[j].ID })

	for _, c := range containers {
		if err := ctx.Err(); err != nil {
			return err
		}
		ref := containerRef(c)
		image := redact.Clean(c.Image)
		if err := sink.Emit(ctx, dockerContainerEdge(hostName, ref, image, at)); err != nil {
			return err
		}
		if image != "" {
			if err := sink.Emit(ctx, containerImageEdge(ref, image, at)); err != nil {
				return err
			}
		}
	}
	return nil
}

// dockerClient builds an HTTP client whose transport dials the daemon's unix
// socket. It is read-only by construction: the connector only ever issues GETs.
func dockerClient(socketPath string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// dockerGET issues a read-only GET to the daemon and returns the response body. A
// non-2xx status is an error. Method is GET unconditionally — this connector
// never writes to the daemon.
func dockerGET(ctx context.Context, client *http.Client, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dockerBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("docker GET %s: status %d", path, resp.StatusCode)
	}
	return body, nil
}

// socketExists reports whether the docker socket path is present on disk. An
// absent path means the daemon is simply not running here, which is a silent skip
// (distinguishing "not present" from "configured but failing").
func socketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// containerRef derives a stable container reference: the first name with its
// leading slash trimmed, falling back to a short id. It is scrubbed for secret
// shapes even though container names rarely carry them (defense in depth).
func containerRef(c dockerContainer) string {
	if len(c.Names) > 0 {
		name := strings.TrimPrefix(c.Names[0], "/")
		if name != "" {
			return redact.Clean(name)
		}
	}
	if len(c.ID) >= 12 {
		return c.ID[:12]
	}
	return c.ID
}
