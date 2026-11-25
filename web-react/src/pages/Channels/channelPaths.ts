export function channelListPath(adminPath: string) {
  return `/${adminPath}/channels`;
}

export function channelNewPath(adminPath: string) {
  return `/${adminPath}/channels/new`;
}

export function channelEditPath(adminPath: string, id: number | string) {
  return `/${adminPath}/channels/edit/${id}`;
}

export function channelAnalysisPath(adminPath: string, id: number | string, from?: 'edit') {
  const path = `/${adminPath}/channels/${id}/analysis`;
  return from === 'edit' ? `${path}?from=edit` : path;
}
