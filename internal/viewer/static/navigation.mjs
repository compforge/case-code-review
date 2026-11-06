// Context belongs to an explicitly selected model call inside Job or Session.
/**
 * @spec Global, Job and Session are workspace views; Context keeps its parent view and native call identity
 * @see {@link component://docs/observability.md}
 */
export function workspaceParams(search) {
  const params = new URLSearchParams(search);
  if (params.get('view') === 'context') {
    params.set('view', 'session');
    params.set('context', '1');
  } else if (!['global', 'job', 'session'].includes(params.get('view'))) {
    params.set('view', 'global');
  }
  return params;
}

export function workspaceURL(base, params, changes) {
  const next = new URLSearchParams(params);
  // Selecting another scope or call closes the old context unless explicitly requested.
  if (!Object.hasOwn(changes, 'context') && ['view', 'session', 'node', 'unit', 'execution', 'tool'].some(key => Object.hasOwn(changes, key))) next.delete('context');
  for (const [key, value] of Object.entries(changes)) value ? next.set(key, value) : next.delete(key);
  return `${base}?${next}`;
}
