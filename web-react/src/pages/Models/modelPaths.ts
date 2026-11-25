export function modelListPath(adminPath: string, source?: 'custom' | 'library') {
  const base = `/${adminPath}/models`;
  return source === 'library' ? `${base}?source=library` : base;
}

export function modelNewPath(adminPath: string) {
  return `/${adminPath}/models/new`;
}

export function modelEditPath(adminPath: string, idOrMid: number | string, kind?: string | null) {
  if (kind === 'catalog') {
    return `/${adminPath}/models/library/edit/${idOrMid}`;
  }
  if (kind === 'unlisted') {
    return `/${adminPath}/models/edit/${idOrMid}?source=library`;
  }
  return `/${adminPath}/models/edit/${idOrMid}`;
}

export function billingRulesListPath(adminPath: string) {
  return `/${adminPath}/billing-rules`;
}

export function billingRulesNewPath(adminPath: string) {
  return `/${adminPath}/billing-rules/new`;
}

export function billingRulesEditPath(adminPath: string, idOrPid: number | string) {
  return `/${adminPath}/billing-rules/edit/${idOrPid}`;
}

export function forwardRulesListPath(adminPath: string) {
  return `/${adminPath}/forward-rules`;
}

export function forwardRulesNewPath(adminPath: string) {
  return `/${adminPath}/forward-rules/new`;
}

export function forwardRulesEditPath(adminPath: string, id: number | string) {
  return `/${adminPath}/forward-rules/edit/${id}`;
}
