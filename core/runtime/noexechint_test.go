// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// ⛔ LA PISTA ES UNA AFIRMACION, NO UN ADORNO: dice «el bit esta puesto, sospecha del montaje».
// Si se emitiera cuando el bit NO esta, mandaria a mirar el sistema de ficheros a alguien cuyo
// problema es un chmod — y en un CI eso son horas. Las dos direcciones se miden aqui.
//
// LA MUTACION: quitar la condicion del modo, o la del tipo de error. Ambas se destapan.
func TestLaPistaDeNoexecSoloSeEmiteConElBitPuestoYUnEACCES(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	conBit := filepath.Join(dir, "con-bit")
	if err := os.WriteFile(conBit, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sinBit := filepath.Join(dir, "sin-bit")
	if err := os.WriteFile(sinBit, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// ⛔ EUID EXPLICITO, NO EL DEL AMBIENTE. Estas aserciones describen el caso SIN privilegios
	// —el binario tiene su bit, sus directorios son atravesables por su dueno, luego lo que queda
	// es el montaje—, y con `noexecHint` leyendo os.Geteuid() dejaban de describirlo en cuanto la
	// suite corria como root. Paso el 2026-08-19 en ci-runner-7, cuyo servicio corre con
	// HOME=/root: `t.TempDir()` es 0700, y para root el bit que manda es el de OTROS, asi que la
	// pista nombraba —correctamente— el directorio en vez del montaje, y este test lo llamo fallo.
	//
	// El defecto no estaba en la pista: estaba en un test que afirmaba sobre una maquina concreta.
	const sinPrivilegios = 1000

	got := noexecHintFor(conBit, syscall.EACCES, sinPrivilegios)
	if !strings.Contains(got, "noexec") || !strings.Contains(got, dir) {
		t.Fatalf("con el bit puesto y EACCES la pista debe nombrar noexec y el directorio; salio %q", got)
	}

	if got := noexecHintFor(sinBit, syscall.EACCES, sinPrivilegios); got != "" {
		t.Fatalf("sin el bit de ejecucion el mensaje de siempre ya apunta bien; la pista sobra: %q", got)
	}

	if got := noexecHintFor(conBit, errors.New("connection refused"), sinPrivilegios); got != "" {
		t.Fatalf("un error que no es de permisos no autoriza a hablar del montaje: %q", got)
	}

	// Y un fichero que no existe: no se inventa nada.
	if got := noexecHintFor(filepath.Join(dir, "no-existe"), syscall.EACCES, sinPrivilegios); got != "" {
		t.Fatalf("sin poder mirar el modo no se emite pista: %q", got)
	}

	// os.ErrPermission envuelto tambien cuenta: es el mismo hecho por otro camino.
	if got := noexecHintFor(conBit, os.ErrPermission, sinPrivilegios); !strings.Contains(got, "noexec") {
		t.Fatalf("os.ErrPermission es la misma causa y debe dar la misma pista; salio %q", got)
	}
}

func TestFakeExecNoexecNamesMountAndOperatorCures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "connector")
	if err := os.WriteFile(bin, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}

	// A bind-mounted noexec fixture needs root. Inject the exact execve result
	// instead, while retaining a real executable path so the diagnostic can prove
	// that it names the selected mount and does not mistake a missing +x bit.
	fakeExec := func(string) error {
		return &os.PathError{Op: "fork/exec", Path: bin, Err: syscall.EACCES}
	}
	got := noexecHintFor(bin, fakeExec(bin), 1000)
	for _, want := range []string{dir, "noexec", "TMPDIR", "--data-dir", "OLIVARES_DATA_DIR"} {
		if !strings.Contains(got, want) {
			t.Fatalf("noexec diagnostic %q does not contain %q", got, want)
		}
	}
}

func TestENOEXECNamesMountAndOperatorCures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "connector")
	if err := os.WriteFile(bin, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}

	got := noexecHintFor(bin, &os.PathError{Op: "fork/exec", Path: bin, Err: syscall.ENOEXEC}, 1000)
	for _, want := range []string{"ENOEXEC", dir, "mount", "TMPDIR", "--data-dir", "OLIVARES_DATA_DIR"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ENOEXEC diagnostic %q does not contain %q", got, want)
		}
	}
}

// ⛔ Y DISTINGUE LA SEGUNDA CAUSA, que es la que se puede arreglar. Un directorio ancestro sin bit
// de BUSQUEDA da el mismo EACCES que un montaje noexec — reproducido el 2026-08-19: con un
// intermedio en `drw-------`, execve responde «permission denied» y rc=126, identico. Si la pista
// nombrara solo el montaje, mandaria a mirar `findmnt` cuando el arreglo esta en el `os.MkdirAll`
// que creo ese directorio.
//
// LA MUTACION: quitar el recorrido de ancestros. Entonces este caso dice «noexec» y falla.
func TestLaPistaSeparaElDirectorioSinBusquedaDelMontajeNoexec(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	medio := filepath.Join(dir, "medio")
	if err := os.MkdirAll(medio, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(medio, "bin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Con todo atravesable, la sospecha honesta es el montaje. euid explicito por lo mismo que en
	// el test de arriba: como root el bit que decide es el de OTROS y un 0700 propio deja de ser
	// «todo atravesable», asi que estas dos aserciones describirian otra maquina.
	const sinPrivilegios = 1000
	got := noexecHintFor(bin, syscall.EACCES, sinPrivilegios)
	if !strings.Contains(got, "noexec") {
		t.Fatalf("con los directorios atravesables la sospecha es el montaje; salio %q", got)
	}

	// Quitando el bit de busqueda al intermedio, la causa es OTRA y comprobable.
	if err := os.Chmod(medio, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(medio, 0o700) })

	got = noexecHintFor(bin, syscall.EACCES, sinPrivilegios)
	if !strings.Contains(got, "BUSQUEDA") || !strings.Contains(got, medio) {
		t.Fatalf("un ancestro sin bit de busqueda debe NOMBRARSE, no atribuirse al montaje; salio %q", got)
	}
	// ⛔ MENCIONAR el montaje no es ATRIBUIRSELO. El mensaje dice «igual que un montaje noexec»
	// como comparacion, y eso ayuda; lo que no puede hacer es SOSPECHAR del montaje cuando ya tiene
	// una causa comprobada. Se comprueba la frase de sospecha, no la palabra.
	if strings.Contains(got, "es probable que") {
		t.Fatalf("con una causa comprobada no se sospecha del montaje: %q", got)
	}
}

// EL BIT QUE SE MIRA DEPENDE DE QUIEN VA A ATRAVESAR, y sin la costura del euid este caso era
// incomprobable: la bateria no corre como root, asi que la rama que importa nunca se ejercitaba.
// Lo encontro otro carril contrastando el instrumento, no una corrida.
func TestElRecorridoDeAncestrosPreguntaPorElUidQueVaAEjecutar(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "extraidos")
	if err := os.MkdirAll(dir, 0o700); err != nil { // lo que deja os.MkdirTemp
		t.Fatalf("mkdir: %v", err)
	}
	bin := filepath.Join(dir, "conector")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o711); err != nil { // binario ya arreglado
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Como ROOT: el hijo sera OTRO uid, y para el un 0700 no es atravesable.
	got := noexecHintFor(bin, syscall.EACCES, 0)
	if !strings.Contains(got, dir) {
		t.Fatalf("como root, la pista no nombra el directorio 0700 que bloquea al uid enjaulado.\n"+
			"pista: %q", got)
	}
	if strings.Contains(got, "todos sus directorios son atravesables") {
		t.Fatalf("como root, la pista AFIRMA que los directorios son atravesables y son 0700: %q", got)
	}

	// Sin privilegios: el hijo hereda el uid del motor, el 0700 SI es atravesable y esta rama
	// no debe disparar — la direccion que no dispara, que es la mitad que se olvida.
	got = noexecHintFor(bin, syscall.EACCES, 1000)
	if strings.Contains(got, "no concede bit de BUSQUEDA") {
		t.Fatalf("sin privilegios, un 0700 propio NO bloquea y la pista no debe acusarlo: %q", got)
	}
}
