export interface ArtifactRendererSpec {
  type: string
  label: string
  markdown: boolean
  versionPicker: boolean
}

const renderers = new Map<string, ArtifactRendererSpec>()

export function registerArtifactRenderer(spec: ArtifactRendererSpec): void {
  renderers.set(spec.type, spec)
}

export function artifactRenderer(type: string): ArtifactRendererSpec {
  return renderers.get(type) || { type, label: type || 'Artifact', markdown: true, versionPicker: true }
}

registerArtifactRenderer({ type: 'plan', label: 'Plan', markdown: true, versionPicker: true })
registerArtifactRenderer({ type: 'todo', label: 'Todo / Phases', markdown: true, versionPicker: true })
registerArtifactRenderer({ type: 'summary', label: 'Summary', markdown: true, versionPicker: true })
registerArtifactRenderer({ type: 'report', label: 'Report', markdown: true, versionPicker: true })
