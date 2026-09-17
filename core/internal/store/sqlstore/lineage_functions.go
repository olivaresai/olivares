// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "strings"

// PostgreSQL's backend lock catalogue is the participation witness. A txid or
// caller GUC alone cannot establish the hierarchy. The routines are owner-only
// bodies with fixed search_path and no caller-selected relation or generation.
func lineageHeldLockSQL(key, mode string) string {
	return `EXISTS (SELECT 1 FROM pg_catalog.pg_locks WHERE locktype='advisory'
 AND pid=pg_catalog.pg_backend_pid() AND granted AND mode='` + mode + `' AND objsubid=1
 AND classid=((pg_catalog.hashtextextended(` + key + `,0)>>32)&4294967295)::oid
 AND objid=(pg_catalog.hashtextextended(` + key + `,0)&4294967295)::oid)`
}
func lineageRoutine(name, args, body string) lineageSQLObject {
	return lineageSQLObject{name: name, arguments: args, result: "void", body: body, exposed: true,
		statement: "CREATE FUNCTION public." + name + "(" + args + ") RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $lineage$" + body + "$lineage$"}
}
func postgresLineageRoutines() []lineageSQLObject {
	exclusive := lineageHeldLockSQL("'"+lineageGateKey+"'", "ExclusiveLock")
	shared := lineageHeldLockSQL("'"+lineageGateKey+"'", "ShareLock")
	tenantLock := lineageHeldLockSQL("'"+lineageTenantKeyPrefix+"' || target_tenant", "ExclusiveLock")
	canonical := `IF target_tenant IS DISTINCT FROM pg_catalog.current_setting('app.tenant_id',true) THEN RAISE EXCEPTION 'lineage target differs from transaction scope'; END IF; IF target_tenant !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' THEN RAISE EXCEPTION 'noncanonical lineage tenant'; END IF;`
	begins := `BEGIN ` + canonical + `
 IF NOT (` + exclusive + ` OR (` + shared + ` AND ` + tenantLock + `)) THEN RAISE EXCEPTION 'lineage gate participation absent'; END IF;
 INSERT INTO public.core_lineage_writer(writer_id,tenant_id) VALUES(pg_catalog.txid_current()::text,target_tenant);
 END;`
	finish := `BEGIN
 DELETE FROM public.core_lineage_writer WHERE writer_id=pg_catalog.txid_current()::text;
 END;`
	exclusiveGuard := `IF NOT ` + exclusive + ` THEN RAISE EXCEPTION 'lineage lifecycle exclusion absent'; END IF;`
	var seeds, drops []string
	for _, r := range lineageRelations {
		seeds = append(seeds, `INSERT INTO public.`+r.descriptor().Table+`(id,tenant_id,created_at,updated_at,version) VALUES(target_tenant,target_tenant,observed_time,observed_time,1);`)
		drops = append(drops, `IF EXISTS(SELECT 1 FROM public.`+r.table+` WHERE tenant_id=target_tenant) THEN RAISE EXCEPTION 'lineage source remains at drop'; END IF; DELETE FROM public.`+r.descriptor().Table+` WHERE tenant_id=target_tenant;`)
	}
	seed := `DECLARE observed_time text; BEGIN ` + exclusiveGuard + canonical + `
 INSERT INTO public.core_lineage_seeded(tenant_id) VALUES(target_tenant);
 observed_time:=pg_catalog.to_char(pg_catalog.clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US')||'000Z';
 ` + strings.Join(seeds, "\n") + ` END;`
	drop := `BEGIN ` + exclusiveGuard + canonical + strings.Join(drops, "\n") + ` END;`
	complete := `BEGIN ` + exclusiveGuard + ` UPDATE public.core_lineage_control SET complete=true WHERE singleton=1; END;`
	routines := []lineageSQLObject{lineageRoutine("olivares_lineage_begin", "target_tenant text", begins), lineageRoutine("olivares_lineage_finish", "", finish), lineageRoutine("olivares_lineage_seed", "target_tenant text", seed), lineageRoutine("olivares_lineage_drop", "target_tenant text", drop), lineageRoutine("olivares_lineage_complete", "", complete)}
	for _, r := range lineageRelations {
		body := `BEGIN ` + canonical + `
  IF NOT EXISTS(SELECT 1 FROM public.core_lineage_writer WHERE tenant_id=target_tenant AND writer_id=pg_catalog.txid_current()::text) THEN RAISE EXCEPTION 'lineage lock writer absent'; END IF;
  PERFORM version FROM public.` + r.descriptor().Table + ` WHERE tenant_id=target_tenant AND id=target_tenant FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'lineage lock epoch absent'; END IF; END;`
		routines = append(routines, lineageRoutine("olivares_lock_"+r.descriptor().Table, "target_tenant text", body))
	}
	return routines
}
