import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { request } from '../api/client'
import type { ModelDTO, ModelsResponse, ProviderDTO } from '../api/types'

// 模型目录：/api/models 的全量投影。
export const useCatalogStore = defineStore('catalog', () => {
  const providers = ref<ProviderDTO[]>([])
  const defaultModel = ref('')
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref('')

  const models = computed<ModelDTO[]>(() => providers.value.flatMap(provider => provider.Models))

  async function load(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const data = await request<ModelsResponse>('/api/models')
      providers.value = Array.isArray(data.Providers) ? data.Providers : []
      const available = models.value
      const preferred = data.DefaultModel || available[0]?.FullID || ''
      defaultModel.value = available.some(model => model.FullID === preferred)
        ? preferred
        : available[0]?.FullID || ''
      loaded.value = true
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : String(cause)
      throw cause
    } finally {
      loading.value = false
    }
  }

  // label 显示 ModelAlias；没有别名时显示全称 id。
  function label(model: ModelDTO): string {
    const alias = (model.Alias || '').trim()
    return alias || model.FullID
  }

  function providerLabel(provider: ProviderDTO): string {
    return (provider.ProviderName || '').trim() || provider.ProviderID
  }

  function find(fullID: string): ModelDTO | undefined {
    return models.value.find(model => model.FullID === fullID)
  }

  return {
    providers,
    models,
    defaultModel,
    loading,
    loaded,
    error,
    load,
    label,
    providerLabel,
    find,
  }
})
