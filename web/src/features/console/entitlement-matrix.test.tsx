// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-07 — «no se sabe» NUNCA es «no». Las tres preguntas y las dos que a menudo no se pueden
// contestar, fijadas donde se deciden: en las funciones puras.
import { describe, expect, it } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import './i18n'
import { ApiError, NetworkError } from '@/lib/api/errors'
import {
  EntitlementMatrix,
  classifyActivationRead,
  classifyLicenseRead,
  ejeActivado,
  ejeBinario,
  ejeDerecho,
} from './entitlement-matrix'

describe('la composición de los tres ejes', () => {
  /**
   * ⛔ EL CONTROL QUE DA SENTIDO A TODA LA PANTALLA: `LicenseStatus.Features` es `omitempty` y sólo
   * llega **con la licencia verificada** (`core/api/license.go:88,92`). Sin licencia verificada no
   * hay nada que atestigüe nada.
   *
   * EL MUTANTE: tratar `features` ausente como lista vacía —un `?? []` en el contenedor basta— y
   * responder «no». Eso convierte «no lo hemos podido comprobar» en «no tienes derecho», que es
   * una afirmación sobre el producto que alguien usa para comprar, escalar o descartar. En la
   * pantalla que responde «¿qué tengo?» es el peor sitio del producto para confundirlas.
   */
  it('sin Features, el derecho es «no se sabe» y NO «no»', () => {
    expect(ejeDerecho('addon_airs', undefined)).toBe('unknown')
  })

  /**
   * ⛔ Y EL SILENCIO DE UNA LISTA PRESENTE TAMPOCO ES UNA NEGATIVA: `Features` es una lista libre y
   * no está verificada contra el catálogo de add-ons, así que una clave que no aparece puede ser
   * un derecho expresado con otro nombre. Sigue siendo «no se sabe».
   */
  it('con Features presente pero sin la clave, sigue siendo «no se sabe»', () => {
    expect(ejeDerecho('addon_airs', ['addon_reg'])).toBe('unknown')
  })

  /** LA DIRECCIÓN QUE NO DEBE DISPARAR: con la clave presente, SÍ hay derecho atestiguado. */
  it('con la clave en Features, hay derecho', () => {
    expect(ejeDerecho('addon_airs', ['addon_reg', 'addon_airs'])).toBe('yes')
  })

  /**
   * ⛔ EL EJE DEL BINARIO ES «no se sabe» POR DISEÑO, y la tentación que rechaza está nombrada en
   * la cabecera: `Preset` dice qué NIVEL introduce cada add-on y sería fácil deducir «community ⇒
   * no está en el binario». Sería inventar la fuente que falta — un preset es empaquetado y un
   * build tag es compilación.
   *
   * EL MUTANTE: derivarlo de la edición. La columna pasaría a afirmar, por módulo, algo que el
   * motor sólo publica por artefacto.
   */
  it('el eje del binario no se deduce de la edición', () => {
    expect(ejeBinario()).toBe('unknown')
  })

  /**
   * Y el eje que SÍ se sabe siempre, con sus cuatro estados: `pending` es «no se sabe» porque el
   * add-on está a la espera de un reinicio — ni encendido ni apagado.
   */
  it('activado distingue los cuatro estados del motor', () => {
    expect(ejeActivado('active')).toBe('yes')
    expect(ejeActivado('pending')).toBe('unknown')
    expect(ejeActivado('available')).toBe('no')
    expect(ejeActivado('console')).toBe('no')
  })
})

describe('la matriz pintada', () => {
  const ADDONS = [
    { key: 'addon_airs', title: 'AIRS', state: 'available' },
    { key: 'addon_reg', title: 'RegOps', state: 'active' },
  ]

  /** Sin licencia verificada la pantalla lo dice ARRIBA, porque condiciona toda la tabla. */
  it('sin licencia verificada avisa de que el derecho no se sabe para todos', () => {
    renderIntel(<EntitlementMatrix addons={ADDONS} edition="community" />)
    expect(
      screen.getByText(/unknown until a verified license/i),
    ).toBeInTheDocument()
  })

  /** Y con licencia verificada ese aviso NO sale: un aviso permanente deja de leerse. */
  it('con licencia verificada no avisa', () => {
    renderIntel(
      <EntitlementMatrix
        addons={ADDONS}
        features={['addon_reg']}
        edition="enterprise"
      />,
    )
    expect(screen.queryByText(/unknown until a verified license/i)).toBeNull()
  })

  it('keeps source distinctions in a closed disclosure, not a primary lecture', async () => {
    const user = userEvent.setup()
    renderIntel(<EntitlementMatrix addons={ADDONS} edition="community" />)
    expect(
      screen.getByRole('columnheader', { name: /in this edition/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: /license coverage/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: /^activation$/i }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/worst screen in the product/i)).toBeNull()
    expect(
      screen.queryByText(/property of the BINARY, not of a module/i),
    ).toBeNull()
    const help = document.querySelector(
      '[data-slot="entitlement-source-help"]',
    ) as HTMLDetailsElement | null
    expect(help).toBeInstanceOf(HTMLDetailsElement)
    expect(help?.open).toBe(false)
    expect(help?.textContent).not.toMatch(/not a loading failure/i)
    expect(help?.textContent).toMatch(
      /not enough information to determine that status/i,
    )
    expect(help?.textContent).toMatch(/status and retry controls/i)
    expect(help?.textContent).toMatch(/running binary/i)
    const summary = help?.querySelector('summary')
    expect(summary).toBeInstanceOf(HTMLElement)
    expect(summary?.tagName).toBe('SUMMARY')
    await user.click(summary as HTMLElement)
    expect(help?.open).toBe(true)
  })

  /**
   * labelled synthetic / partial-missing-features: a successful catalogue with
   * `features` undefined must keep entitlement unknown, never "none".
   */
  it('labelled partial-missing-features: Features undefined stays unknown, not empty-no', () => {
    renderIntel(<EntitlementMatrix addons={ADDONS} edition="community" />)
    expect(
      screen.getByText(/unknown until a verified license/i),
    ).toBeInTheDocument()
    expect(
      screen.queryAllByText('entitled').filter((n) => n.tagName !== 'TH'),
    ).toHaveLength(0)
  })

  it('failed refresh copy is not "no verified licence" and does not paint entitled', () => {
    renderIntel(
      <EntitlementMatrix
        addons={ADDONS}
        edition="community"
        entitlementUnknownReason="refresh-failed"
        lastSuccessfulLicense={{ edition: 'community', status: 'valid' }}
      />,
    )
    expect(screen.queryByText(/unknown until a verified license/i)).toBeNull()
    expect(screen.getByText(/could not be refreshed/i)).toBeInTheDocument()
    expect(screen.getByText(/Last retrieved/i)).toBeInTheDocument()
    expect(
      screen.queryAllByText('entitled').filter((n) => n.tagName !== 'TH'),
    ).toHaveLength(0)
    const help = document.querySelector(
      '[data-slot="entitlement-source-help"]',
    ) as HTMLDetailsElement | null
    expect(help?.textContent).not.toMatch(/not a loading failure/i)
    expect(help?.textContent).toMatch(
      /not enough information to determine that status/i,
    )
  })

  it('keeps a long add-on key as a row header with every column labelled', () => {
    const key = 'addon_very_long_synthetic_key_name_for_narrow_layouts'
    renderIntel(
      <EntitlementMatrix
        addons={[{ key, title: 'Long synthetic add-on', state: 'pending' }]}
        features={[key]}
        edition="enterprise"
      />,
    )
    expect(screen.getByRole('rowheader', { name: key })).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: /in this edition/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: /license coverage/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: /^activation$/i }),
    ).toBeInTheDocument()
    expect(screen.getByText('entitled')).toBeInTheDocument()
    expect(screen.getByText('pending restart')).toBeInTheDocument()
    expect(
      document.querySelector('[data-slot="entitlement-matrix-table"]'),
    ).toBeTruthy()
    expect(
      document
        .querySelector('[data-slot="entitlement-matrix-frame"]')
        ?.classList.contains('min-w-0'),
    ).toBe(true)
  })

  it('no prior license success does not claim there is no license', () => {
    renderIntel(
      <EntitlementMatrix
        addons={ADDONS}
        edition="community"
        entitlementUnknownReason="unavailable"
      />,
    )
    expect(screen.queryByText(/unknown until a verified license/i)).toBeNull()
    expect(
      screen.getByText(/Current license information is unavailable/i),
    ).toBeInTheDocument()
    expect(
      screen.queryAllByText('entitled').filter((n) => n.tagName !== 'TH'),
    ).toHaveLength(0)
  })
})

describe('classifyActivationRead — labelled catalogue states', () => {
  const ready = {
    edition: 'enterprise',
    restart_required: true,
    addons: [
      {
        key: 'addon_airs',
        title: 'AIRS',
        summary: 'synthetic',
        env: '',
        preset: 'starter',
        state: 'available',
      },
    ],
    presets: [],
  }

  it('labelled loading: pending with no data is loading, not empty', () => {
    expect(
      classifyActivationRead({
        isPending: true,
        isError: false,
        error: null,
        data: undefined,
      }),
    ).toEqual({ kind: 'loading' })
  })

  it('labelled community 501: open-core seam is unavailable, not empty', () => {
    expect(
      classifyActivationRead({
        isPending: false,
        isError: true,
        error: new ApiError(
          501,
          'activation_unavailable',
          'Activation is not wired on this deployment.',
        ),
        data: undefined,
      }),
    ).toEqual({
      kind: 'unavailable',
      status: 501,
      code: 'activation_unavailable',
      requestId: undefined,
      hadPriorData: false,
    })
  })

  it('labelled error: a non-501 failure is failed read, not empty', () => {
    expect(
      classifyActivationRead({
        isPending: false,
        isError: true,
        error: new ApiError(500, 'internal', 'boom'),
        data: undefined,
      }).kind,
    ).toBe('failed')
    expect(
      classifyActivationRead({
        isPending: false,
        isError: true,
        error: new NetworkError('offline'),
        data: undefined,
      }).kind,
    ).toBe('failed')
  })

  it('labelled empty: successful addons=[] is known empty, not 501', () => {
    expect(
      classifyActivationRead({
        isPending: false,
        isError: false,
        error: null,
        data: {
          edition: 'community',
          restart_required: true,
          addons: [],
          presets: [],
        },
      }),
    ).toEqual({ kind: 'empty' })
  })

  it('labelled catalog success: addons present is ready', () => {
    const got = classifyActivationRead({
      isPending: false,
      isError: false,
      error: null,
      data: ready,
    })
    expect(got.kind).toBe('ready')
    if (got.kind === 'ready') expect(got.addons).toHaveLength(1)
  })

  it('null or missing addons is a failed read, not known empty', () => {
    expect(
      classifyActivationRead({
        isPending: false,
        isError: false,
        error: null,
        data: {
          edition: 'community',
          restart_required: true,
          // @ts-expect-error labelled malformed payload: addons omitted
          addons: undefined,
          presets: [],
        },
      }).kind,
    ).toBe('failed')
    expect(
      classifyActivationRead({
        isPending: false,
        isError: false,
        error: null,
        data: {
          edition: 'community',
          restart_required: true,
          // @ts-expect-error labelled malformed payload: addons null
          addons: null,
          presets: [],
        },
      }).kind,
    ).toBe('failed')
  })

  it('stale successful data plus error is failed/unavailable, not current truth', () => {
    const stale = classifyActivationRead({
      isPending: false,
      isError: true,
      error: new ApiError(500, 'internal', 'later'),
      data: ready,
    })
    expect(stale).toMatchObject({ kind: 'failed', hadPriorData: true })
    const stale501 = classifyActivationRead({
      isPending: false,
      isError: true,
      error: new ApiError(501, 'activation_unavailable', 'seam'),
      data: ready,
    })
    expect(stale501).toMatchObject({
      kind: 'unavailable',
      hadPriorData: true,
      status: 501,
    })
  })
})

describe('classifyLicenseRead — failed refresh is not current success', () => {
  const valid = {
    edition: 'community',
    hot_apply: true,
    status: 'valid',
    source: 'data_dir',
    managed_externally: false,
    max_users: 0,
    seat_limit: 0,
    seat_limited: false,
    active_users: 1,
    features: ['addon_airs'],
  }

  it('pending with no data is loading, not "no license"', () => {
    expect(
      classifyLicenseRead({
        isPending: true,
        isError: false,
        error: null,
        data: undefined,
      }),
    ).toEqual({ kind: 'loading' })
  })

  it('successful payload is current success, including omitted features', () => {
    const got = classifyLicenseRead({
      isPending: false,
      isError: false,
      error: null,
      data: { ...valid, features: undefined, status: 'none' },
    })
    expect(got.kind).toBe('success')
    if (got.kind === 'success') {
      expect(got.license.features).toBeUndefined()
      expect(got.license.status).toBe('none')
    }
  })

  it('error with prior data is failed last-success, not current entitled facts', () => {
    const got = classifyLicenseRead({
      isPending: false,
      isError: true,
      error: new ApiError(500, 'internal', 'later'),
      data: valid,
    })
    expect(got).toMatchObject({
      kind: 'failed',
      hadPriorData: true,
      status: 500,
      lastSuccess: valid,
    })
  })

  it('error with no prior data is failed unavailable, not "no license"', () => {
    const got = classifyLicenseRead({
      isPending: false,
      isError: true,
      error: new ApiError(500, 'internal', 'first'),
      data: undefined,
    })
    expect(got).toMatchObject({
      kind: 'failed',
      hadPriorData: false,
      lastSuccess: undefined,
      status: 500,
    })
  })
})
