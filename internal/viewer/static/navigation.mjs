// Context belongs to an explicitly selected model call inside Job or Session.
/**
 * @spec Job and Session lists require explicit item selection; Session tabs survive reload; evidence links preserve native identity and open Full Session
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
  if (params.get('view') === 'session') {
    if (['node', 'execution', 'tool', 'context'].some(key => params.get(key))) params.set('tab', 'full');
    else if (!['overview', 'full'].includes(params.get('tab'))) params.set('tab', 'overview');
  }
  return params;
}

export function workspaceURL(base, params, changes) {
  const next = new URLSearchParams(params);
  // Selecting another scope or call closes the old context unless explicitly requested.
  if (!Object.hasOwn(changes, 'context') && ['view', 'session', 'node', 'unit', 'execution', 'tool'].some(key => Object.hasOwn(changes, key))) next.delete('context');
  for (const [key, value] of Object.entries(changes)) value ? next.set(key, value) : next.delete(key);
  if (next.get('view') === 'session' && ['node', 'execution', 'tool', 'context'].some(key => changes[key])) next.set('tab', 'full');
  return `${base}?${next}`;
}
