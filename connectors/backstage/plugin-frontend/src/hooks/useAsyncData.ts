// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from 'react';

/** The state of an in-flight or settled async read. */
export interface AsyncState<T> {
  loading: boolean;
  value?: T;
  error?: Error;
}

/**
 * useAsyncData runs an async reader and tracks loading/value/error, re-running
 * when `deps` change or `retry()` is called. It guards against setting state after
 * unmount or after a superseding run, so a slow control-plane read never lands on
 * a stale component. A tiny local hook keeps the plugin's dependency surface to
 * just react + @backstage (no react-use), which matters for an unpinned plugin.
 */
export function useAsyncData<T>(
  fn: () => Promise<T>,
  deps: ReadonlyArray<unknown> = [],
): AsyncState<T> & { retry: () => void } {
  const [state, setState] = useState<AsyncState<T>>({ loading: true });
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    let active = true;
    setState(prev => ({ ...prev, loading: true, error: undefined }));
    fn().then(
      value => {
        if (active) {
          setState({ loading: false, value });
        }
      },
      (error: Error) => {
        if (active) {
          setState({ loading: false, error });
        }
      },
    );
    return () => {
      active = false;
    };
    // fn is intentionally excluded; callers pass a stable `deps` list.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const retry = useCallback(() => setNonce(n => n + 1), []);
  return { ...state, retry };
}
