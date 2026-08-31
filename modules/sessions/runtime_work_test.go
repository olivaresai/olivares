// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRunHasWorkBindingFailsClosedOnEveryPartialStamp(t *testing.T) {
	t.Parallel()

	if runHasWorkBinding(model.Record{}) {
		t.Fatal("a historical run with all work columns NULL was classified as work-bound")
	}
	cases := []struct {
		name  string
		field string
		value any
	}{
		{name: "item", field: colRunWorkItemID, value: model.NewID().String()},
		{name: "fence", field: colRunWorkLeaseFence, value: int64(7)},
		{name: "dispatch", field: colRunWorkDispatchKey, value: []byte{0x01}},
		{name: "owner epoch", field: colRunWorkOwnerEpoch, value: int64(3)},
		{name: "launch spec", field: colRunWorkLaunchSpecHash, value: hashBytes([]byte("spec"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !runHasWorkBinding(model.Record{tc.field: tc.value}) {
				t.Fatalf("a partial work stamp containing %s fell through as legacy", tc.field)
			}
		})
	}
}

func TestRunDTOWorkBindingIsVisibleAndDispatchKeyIsHex(t *testing.T) {
	t.Parallel()

	m := New()
	rec := model.Record{
		colState:              stateRunning,
		colRunWorkItemID:      model.NewID().String(),
		colRunWorkLeaseFence:  int64(11),
		colRunWorkDispatchKey: []byte{0x00, 0xab, 0xff},
		colRunWorkOwnerEpoch:  int64(5),
	}
	dto := m.toRunDTO(rec)
	if dto.WorkItemID.String() != rec.String(colRunWorkItemID) ||
		dto.WorkLeaseFence == nil || *dto.WorkLeaseFence != 11 ||
		dto.WorkDispatchKey != "00abff" ||
		dto.WorkOwnerEpoch == nil || *dto.WorkOwnerEpoch != 5 {
		t.Fatalf("work binding projection lost provenance: %+v", dto)
	}

	legacy := m.toRunDTO(model.Record{colState: stateRunning})
	if !legacy.WorkItemID.IsZero() || legacy.WorkLeaseFence != nil ||
		legacy.WorkDispatchKey != "" || legacy.WorkOwnerEpoch != nil {
		t.Fatalf("legacy NULL binding did not stay empty: %+v", legacy)
	}
}

func TestLegacyRuntimeControlRejectsWorkBoundRunWithoutProcessEffects(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	bindRunForWorkTest(t, m, tenant, dto.RunRef, model.NewID(), 1, hashBytes([]byte("dispatch-a")), 1)

	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(`{"type":"user"}`)); !isRunConflict(err) {
		t.Fatalf("legacy input should reject a work-bound run with 409, got %v", err)
	}
	if got := fr.lastProc().sentCount(); got != 0 {
		t.Fatalf("legacy input reached the work-bound process %d time(s)", got)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser); !isRunConflict(err) {
		t.Fatalf("legacy stop should reject a work-bound run with 409, got %v", err)
	}
	fr.lastProc().mu.Lock()
	stopped := fr.lastProc().done
	fr.lastProc().mu.Unlock()
	if stopped {
		t.Fatal("legacy stop terminated the work-bound process")
	}
	finishWorkRuntimeRun(t, m, tenant, dto.RunRef, fr.lastProc())
}

func TestLegacyResumeRejectsWorkBoundRunAndStillAllowsLegacyRun(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	createStopped := func(name, providerSID string) runDTO {
		t.Helper()
		fr.mu.Lock()
		fr.initSID = providerSID
		fr.mu.Unlock()
		dto, err := m.createRun(ctx, tenant, CreateRunParams{
			Name: name, Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: model.ActorUser,
		})
		if err != nil {
			t.Fatalf("createRun(%s): %v", name, err)
		}
		waitFor(t, name+" provider session id", func() bool {
			got, getErr := m.getRun(ctx, tenant, dto.RunRef)
			return getErr == nil && got.ClaudeSessionID == providerSID
		})
		stopped, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", model.ActorUser)
		if err != nil || stopped.State != stateStopped {
			t.Fatalf("stopRun(%s) = %+v, %v", name, stopped, err)
		}
		return stopped
	}
	launchCount := func() int {
		fr.mu.Lock()
		defer fr.mu.Unlock()
		return len(fr.specs)
	}

	bound := createStopped("work-bound", "provider-work-bound")
	bindRunForWorkTest(
		t, m, tenant, bound.RunRef, model.NewID(), 7,
		hashBytes([]byte("dispatch-work-bound")), 1,
	)
	before := launchCount()
	if _, err := m.resumeRun(ctx, tenant, bound.RunRef, "user:u1", model.ActorUser, ""); !isRunConflict(err) {
		t.Fatalf("legacy resume should reject a work-bound run with 409, got %v", err)
	}
	if got := launchCount(); got != before {
		t.Fatalf("rejected work-bound resume launched %d process(es), want 0", got-before)
	}

	legacy := createStopped("legacy", "provider-legacy")
	before = launchCount()
	resumed, err := m.resumeRun(ctx, tenant, legacy.RunRef, "user:u1", model.ActorUser, "")
	if err != nil || resumed.State != stateRunning {
		t.Fatalf("resume legacy run = %+v, %v", resumed, err)
	}
	if got := launchCount(); got != before+1 {
		t.Fatalf("legacy resume launch count = %d, want %d", got, before+1)
	}
	finishWorkRuntimeRun(t, m, tenant, legacy.RunRef, fr.lastProc())
}

func TestRunDispatchKeyIsUniquePerTenantAndAllowsDistinctKeys(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	var otherTenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "dispatch-key-other", Slug: "dispatch-key-other", Status: model.StatusActive,
		})
		otherTenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("create second tenant: %v", err)
	}
	create := func(runTenant model.TenantID, actor string) runDTO {
		dto, err := m.createRun(ctx, runTenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: actor, ActorKind: model.ActorUser,
		})
		if err != nil {
			t.Fatalf("createRun(%s): %v", actor, err)
		}
		return dto
	}
	first, second := create(tenant, "user:u1"), create(tenant, "user:u2")
	other := create(otherTenant, "user:u3")
	keyA, keyB := hashBytes([]byte("dispatch-a")), hashBytes([]byte("dispatch-b"))
	bindRunForWorkTest(t, m, tenant, first.RunRef, model.NewID(), 1, keyA, 1)
	bindRunForWorkTest(t, m, tenant, second.RunRef, model.NewID(), 1, keyB, 1)
	// Non-trigger direction: uniqueness is tenant-scoped. The same deterministic
	// dispatch key in another tenant must not become a global deny-all lock.
	bindRunForWorkTest(t, m, otherTenant, other.RunRef, model.NewID(), 1, keyA, 1)

	err := mutateRunForWorkTest(m, tenant, second.RunRef, func(rec model.Record) {
		rec[colRunWorkDispatchKey] = append([]byte(nil), keyA...)
	})
	if err == nil {
		t.Fatal("duplicate tenant+work_dispatch_key was accepted")
	}
	if !errors.Is(err, store.ErrConflict) {
		t.Logf("duplicate dispatch key returned backend-specific constraint error: %v", err)
	}
	finishWorkRuntimeRun(t, m, tenant, first.RunRef, fr.procs[0])
	finishWorkRuntimeRun(t, m, tenant, second.RunRef, fr.procs[1])
	finishWorkRuntimeRun(t, m, otherTenant, other.RunRef, fr.procs[2])
}

func bindRunForWorkTest(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	runRef string,
	itemID model.ID,
	fence int64,
	dispatchKey []byte,
	ownerEpoch int64,
) {
	t.Helper()
	if err := mutateRunForWorkTest(m, tenant, runRef, func(rec model.Record) {
		rec[colRunWorkItemID] = itemID.String()
		rec[colRunWorkLeaseFence] = fence
		rec[colRunWorkDispatchKey] = append([]byte(nil), dispatchKey...)
		rec[colRunWorkOwnerEpoch] = ownerEpoch
	}); err != nil {
		t.Fatalf("bind run %s for work: %v", runRef, err)
	}
}

func mutateRunForWorkTest(
	m *Module,
	tenant model.TenantID,
	runRef string,
	mutate func(model.Record),
) error {
	ctx := context.Background()
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		mutate(rec)
		_, err = repo.Update(ctx, rec)
		return err
	})
}

// finalizeWaitBudget is how long the helpers below wait for a run to finalize.
//
// ⛔ ES UNA RED DE SEGURIDAD, NO UN PRESUPUESTO, y la diferencia se midió. Entre que el proceso
// muere y que se cierra `finalizedCh` hay TRES viajes al store —`revokeLiveRuntimeCredentials`,
// `OwnerDied` y `transition` (runtime_bridge.go:253, :273, :279)—, así que este plazo no espera
// una señal barata: espera trabajo de base de datos.
//
// Medido el 2026-08-24 sobre `TestLaunchForWorkReplaySurvivesStoreReopen`, que ejercita esta
// ruta, con el árbol verificado idéntico a `origin/main`:
//
//	sin -race   20 corridas   0,72-1,00 s   media 0,83 s
//	con -race   10 corridas   9,54-11,36 s  media 10,40 s      ->  factor 12,5x
//
// El factor está POR ENCIMA del extremo de la calibración por paquete del repositorio
// (1,52-11,27x), o sea que extrapolarla se quedaba corto. Con 3 s fijos, una finalización que
// sin `-race` cueste ~240 ms llega al plazo con `-race`: el margen no era holgado, era el borde.
// De ahí la ocurrencia aislada de «did not finalize» bajo `-race` y ninguna sin él.
//
// Se declara como variable para que un test pueda ENCOGERLA y demostrar que el plazo es lo que
// corta —ver TestTheFinalizeWaitIsABackstopAndNotABudget—. Nadie debe subirla para "arreglar" un
// rojo: subirla convierte un rojo ruidoso en una espera larga que acaba en el mismo sitio.
var finalizeWaitBudget = 3 * time.Second

// waitWorkRuntimeFinalized informa SI el run finalizó dentro del plazo, sin matar al llamante.
// Existe separada de finishWorkRuntimeRun porque un `t.Fatalf` no se puede observar: para probar
// que el plazo es lo que corta hace falta un camino que DEVUELVA el veredicto.
func waitWorkRuntimeFinalized(lr *liveRun, budget time.Duration) bool {
	select {
	case <-lr.finalizedCh:
		return true
	case <-time.After(budget):
		return false
	}
}

func finishWorkRuntimeRun(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	runRef string,
	proc *fakeProc,
) {
	t.Helper()
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		t.Fatalf("run %s has no live handle to finish", runRef)
	}
	proc.finish(0)
	start := time.Now()
	ok = waitWorkRuntimeFinalized(lr, finalizeWaitBudget)
	elapsed := time.Since(start)
	// ⛔ CADA LLAMANTE PUBLICA SU PROPIO TRAMO, y no el del test entero. Medir el test completo
	// mezcla el montaje con lo que este plazo vigila: en el arnés en memoria el montaje es el
	// 99 % del tiempo, así que un factor sacado del total no dice nada de este tramo. Con la
	// línea aquí, el margen real queda en el log de CUALQUIER caso que use el ayudante —
	// incluidos los que abren SQLite EN FICHERO, que es donde el plazo tiene menos aire.
	t.Logf("FINALIZE_WAIT|run=%s|elapsed=%s|budget=%s|ok=%v", runRef, elapsed, finalizeWaitBudget, ok)
	if !ok {
		t.Fatalf("run %s did not finalize within %s", runRef, finalizeWaitBudget)
	}
}
