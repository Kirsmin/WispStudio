(() => {
  'use strict'

  const $ = (id) => document.getElementById(id)
  const els = {
    sidebar: $('sidebar'),
    connectionButton: $('connectionButton'),
    connectionDot: $('connectionDot'),
    connectionText: $('connectionText'),
    newSessionButton: $('newSessionButton'),
    sessionItems: $('sessionItems'),
    notConnected: $('notConnected'),
    chatConnected: $('chatConnected'),
    centerConnectButton: $('centerConnectButton'),
    messagesScroller: $('messagesScroller'),
    emptyChat: $('emptyChat'),
    messageList: $('messageList'),
    notice: $('notice'),
    composerInput: $('composerInput'),
    modelSelect: $('modelSelect'),
    thinkingSelect: $('thinkingSelect'),
    sendButton: $('sendButton'),
    sendIcon: $('sendIcon'),
    stopIcon: $('stopIcon'),
    connectModal: $('connectModal'),
    serverUrlInput: $('serverUrlInput'),
    connectError: $('connectError'),
    connectCancel: $('connectCancel'),
    connectSubmit: $('connectSubmit'),
    renameModal: $('renameModal'),
    renameInput: $('renameInput'),
    renameCancel: $('renameCancel'),
    renameSubmit: $('renameSubmit'),
    toastRoot: $('toastRoot'),
  }

  const state = {
    connected: false,
    connecting: false,
    serverUrl: normalizeURL(localStorage.getItem('wisp_server_url') || window.location.origin),
    models: [],
    sessions: [],
    currentSessionId: localStorage.getItem('wisp_current_session_id') || '',
    messages: [],
    selectedModel: localStorage.getItem('wisp_selected_model') || '',
    selectedThinking: 'off',
    thinkingByModel: readJSONStorage('wisp_thinking_by_model', {}),
    streaming: false,
    background: false,
    streamSessionId: '',
    controller: null,
    viewToken: 0,
    pollTimer: null,
    renderRAF: null,
    expandedReasoning: new Set(),
    renameTarget: '',
    disconnectArmedUntil: 0,
  }

  class SSEParser {
    constructor() { this.buffer = '' }
    feed(chunk) {
      this.buffer += chunk
      this.buffer = this.buffer.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
      const events = []
      let boundary = this.buffer.indexOf('\n\n')
      while (boundary >= 0) {
        const block = this.buffer.slice(0, boundary)
        this.buffer = this.buffer.slice(boundary + 2)
        const parsed = this.parseBlock(block)
        if (parsed) events.push(parsed)
        boundary = this.buffer.indexOf('\n\n')
      }
      return events
    }
    flush() {
      const block = this.buffer.trim()
      this.buffer = ''
      const parsed = this.parseBlock(block)
      return parsed ? [parsed] : []
    }
    parseBlock(block) {
      if (!block) return null
      let event = 'message'
      const data = []
      for (const line of block.split('\n')) {
        if (line.startsWith(':')) continue
        if (line.startsWith('event:')) event = line.slice(6).trim()
        if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
      }
      if (!data.length) return null
      return { event, data: data.join('\n') }
    }
  }

  function normalizeURL(value) {
    let url = String(value || '').trim()
    if (!url) return window.location.origin
    if (!/^https?:\/\//i.test(url)) url = `http://${url}`
    return url.replace(/\/+$/, '')
  }

  function apiURL(path) { return `${state.serverUrl}${path}` }

  async function fetchJSON(path, options = {}) {
    const response = await fetch(apiURL(path), { cache: 'no-store', ...options })
    if (!response.ok) throw new Error(await readHTTPError(response, `请求失败 (${response.status})`))
    if (response.status === 204) return null
    return response.json()
  }

  async function readHTTPError(response, fallback) {
    try {
      const type = response.headers.get('content-type') || ''
      if (type.includes('application/json')) {
        const data = await response.json()
        return data.error || fallback
      }
      return (await response.text()).trim() || fallback
    } catch { return fallback }
  }

  function readJSONStorage(key, fallback) {
    try { return JSON.parse(localStorage.getItem(key) || '') || fallback } catch { return fallback }
  }

  function toast(message, type = 'info') {
    const node = document.createElement('div')
    node.className = `toast ${type === 'error' ? 'error' : ''}`
    node.textContent = message
    els.toastRoot.appendChild(node)
    window.setTimeout(() => node.remove(), 3200)
  }

  function setNotice(message) {
    els.notice.textContent = message || ''
    els.notice.classList.toggle('hidden', !message)
  }

  function renderConnection() {
    els.sidebar.classList.toggle('disabled', !state.connected)
    els.newSessionButton.disabled = !state.connected
    els.notConnected.classList.toggle('hidden', state.connected)
    els.chatConnected.classList.toggle('hidden', !state.connected)
    els.connectionDot.classList.remove('connected', 'error')
    if (state.connected) {
      els.connectionDot.classList.add('connected')
      els.connectionText.textContent = '已连接'
    } else if (state.connecting) {
      els.connectionText.textContent = '连接中…'
    } else {
      els.connectionText.textContent = '连接服务器'
    }
    renderComposerState()
  }

  function renderSessions() {
    els.sessionItems.replaceChildren()
    for (const session of state.sessions) {
      const item = document.createElement('div')
      item.className = `session-item${session.id === state.currentSessionId ? ' active' : ''}`
      item.dataset.sessionId = session.id

      const title = document.createElement('span')
      title.className = 'session-title'
      title.textContent = session.title || '新会话'
      title.title = session.title || '新会话'

      const menu = document.createElement('button')
      menu.type = 'button'
      menu.className = 'session-menu'
      menu.textContent = '···'
      menu.setAttribute('aria-label', '会话菜单')
      menu.addEventListener('click', (event) => {
        event.stopPropagation()
        showSessionMenu(menu, session)
      })

      item.append(title, menu)
      item.addEventListener('click', () => { void openSession(session.id) })
      els.sessionItems.appendChild(item)
    }
  }

  function showSessionMenu(anchor, session) {
    document.querySelectorAll('.session-popover').forEach((node) => node.remove())
    const popover = document.createElement('div')
    popover.className = 'session-popover'
    const rename = document.createElement('button')
    rename.type = 'button'
    rename.textContent = '重命名'
    const remove = document.createElement('button')
    remove.type = 'button'
    remove.textContent = '删除'
    remove.className = 'danger'
    rename.addEventListener('click', () => {
      popover.remove()
      state.renameTarget = session.id
      els.renameInput.value = session.title || ''
      showModal(els.renameModal)
      window.setTimeout(() => { els.renameInput.focus(); els.renameInput.select() }, 0)
    })
    remove.addEventListener('click', async () => {
      popover.remove()
      if (!window.confirm(`确定要删除会话「${session.title || '新会话'}」吗？`)) return
      try {
        await fetchJSON(`/api/sessions/${encodeURIComponent(session.id)}`, { method: 'DELETE' })
        if (state.currentSessionId === session.id) newConversation()
        await loadSessions()
        toast('已删除')
      } catch (error) { toast(errorMessage(error), 'error') }
    })
    popover.append(rename, remove)
    document.body.appendChild(popover)
    const rect = anchor.getBoundingClientRect()
    popover.style.top = `${Math.min(window.innerHeight - 92, rect.bottom + 4)}px`
    popover.style.left = `${Math.max(8, rect.right - 104)}px`
    const close = (event) => {
      if (!popover.contains(event.target)) {
        popover.remove()
        document.removeEventListener('pointerdown', close, true)
      }
    }
    window.setTimeout(() => document.addEventListener('pointerdown', close, true), 0)
  }

  function renderModels() {
    const previous = state.selectedModel
    els.modelSelect.replaceChildren()
    for (const model of state.models) {
      const option = document.createElement('option')
      option.value = model.id
      option.textContent = model.name || model.id
      els.modelSelect.appendChild(option)
    }
    if (!state.models.length) {
      state.selectedModel = ''
    } else if (state.models.some((model) => model.id === previous)) {
      state.selectedModel = previous
    } else {
      state.selectedModel = state.models.find((model) => model.default)?.id || state.models[0].id
    }
    els.modelSelect.value = state.selectedModel
    if (state.selectedModel) localStorage.setItem('wisp_selected_model', state.selectedModel)
    syncThinkingForModel()
  }

  function syncThinkingForModel() {
    const model = state.models.find((item) => item.id === state.selectedModel)
    const levels = normalizeLevels(model?.thinking_levels)
    const saved = state.thinkingByModel[state.selectedModel]
    state.selectedThinking = levels.includes(saved) ? saved : (levels.includes('off') ? 'off' : levels[0])
    els.thinkingSelect.replaceChildren()
    for (const level of levels) {
      const option = document.createElement('option')
      option.value = level
      option.textContent = thinkingLabel(level)
      els.thinkingSelect.appendChild(option)
    }
    els.thinkingSelect.value = state.selectedThinking
    els.thinkingSelect.disabled = levels.length <= 1 || state.streaming || state.background
    els.thinkingSelect.title = levels.length <= 1 ? '当前模型未声明可调思考深度；可在 config.toml.local 中覆盖 thinking_levels' : '思考深度'
    renderComposerState()
  }

  function normalizeLevels(levels) {
    if (!Array.isArray(levels) || levels.length === 0) return ['off']
    const result = []
    for (const raw of levels) {
      const value = String(raw || '').toLowerCase().trim()
      if (value && !result.includes(value)) result.push(value)
    }
    return result.length ? result : ['off']
  }

  function thinkingLabel(level) {
    const labels = {
      off: '关闭', none: '关闭', on: '开启', auto: '自动', minimal: '最小',
      low: '低', medium: '中', high: '高', xhigh: '极高',
    }
    return labels[level] || level
  }

  function renderComposerState() {
    const busy = state.streaming || state.background
    els.modelSelect.disabled = busy || !state.connected || !state.models.length
    const model = state.models.find((item) => item.id === state.selectedModel)
    const levels = normalizeLevels(model?.thinking_levels)
    els.thinkingSelect.disabled = busy || !state.connected || levels.length <= 1
    els.composerInput.disabled = !state.connected || state.background
    els.sendButton.classList.toggle('streaming', busy)
    els.sendIcon.classList.toggle('hidden', busy)
    els.stopIcon.classList.toggle('hidden', !busy)
    els.sendButton.title = busy ? '停止生成' : '发送 (Enter)'
    els.sendButton.setAttribute('aria-label', busy ? '停止生成' : '发送')
    els.sendButton.disabled = !busy && (!state.connected || !state.selectedModel || !els.composerInput.value.trim())
    if (!state.connected) els.composerInput.placeholder = '请先连接服务器'
    else if (state.background) els.composerInput.placeholder = '该会话仍在生成，请先停止或等待完成'
    else els.composerInput.placeholder = '向 Wisp 发送消息…'
  }

  function renderMessages(forceScroll = false) {
    const shouldStick = forceScroll || isNearBottom()
    els.messageList.replaceChildren()
    els.emptyChat.classList.toggle('hidden', state.messages.length > 0 || state.background)
    if (!state.messages.length && state.currentSessionId) {
      els.emptyChat.querySelector('.empty-title').textContent = '这个会话还没有消息'
      els.emptyChat.querySelector('.empty-sub').textContent = '发送第一条消息，内容会实时显示在这里。'
    } else {
      els.emptyChat.querySelector('.empty-title').textContent = '开始一段对话'
      els.emptyChat.querySelector('.empty-sub').textContent = '消息将发送给当前选中的模型'
    }

    for (const message of state.messages) {
      els.messageList.appendChild(createMessageNode(message))
    }
    if (shouldStick) requestAnimationFrame(() => { els.messagesScroller.scrollTop = els.messagesScroller.scrollHeight })
  }

  function scheduleRender() {
    if (state.renderRAF != null) return
    state.renderRAF = requestAnimationFrame(() => {
      state.renderRAF = null
      renderMessages(false)
    })
  }

  function createMessageNode(message) {
    const wrapper = document.createElement('div')
    wrapper.className = `message-item ${message.type}`
    const content = document.createElement('div')
    content.className = 'message-content'

    if (message.type === 'user') {
      content.textContent = message.content || ''
      wrapper.appendChild(content)
      return wrapper
    }

    const streaming = message.status === 'streaming'
    const hasReasoning = Boolean(message.reasoning) || streaming
    if (hasReasoning) {
      const block = document.createElement('div')
      block.className = 'reasoning-block'
      const toggle = document.createElement('button')
      toggle.type = 'button'
      toggle.className = 'reasoning-toggle'
      const autoExpanded = streaming && !message.content
      const expanded = state.expandedReasoning.has(message.id) || autoExpanded
      toggle.innerHTML = streaming && !message.content
        ? `思考中 <span class="thinking-spinner"></span>`
        : `思考 ${expanded ? '▼' : '▶'}${message.ttft_ms ? ` <span class="reasoning-duration">首字 ${Math.round(message.ttft_ms)}ms</span>` : ''}`
      const reasoning = document.createElement('div')
      reasoning.className = 'reasoning-text'
      reasoning.textContent = message.reasoning || '思考中...'
      reasoning.classList.toggle('hidden', !expanded)
      toggle.addEventListener('click', () => {
        if (state.expandedReasoning.has(message.id)) state.expandedReasoning.delete(message.id)
        else state.expandedReasoning.add(message.id)
        renderMessages(false)
      })
      block.append(toggle, reasoning)
      content.appendChild(block)
    }

    if (message.content) {
      const body = document.createElement('div')
      body.className = 'message-body'
      body.innerHTML = renderMarkdown(message.content)
      content.appendChild(body)
    } else if (streaming && !message.reasoning) {
      const waiting = document.createElement('div')
      waiting.className = 'thinking-placeholder'
      waiting.innerHTML = '<span class="thinking-spinner"></span> 正在等待模型响应…'
      content.appendChild(waiting)
    }

    if (message.error) {
      const error = document.createElement('div')
      error.className = 'error-text'
      error.textContent = message.error
      content.appendChild(error)
    }

    if (message.finish && !['stop', 'length', 'aborted', 'error'].includes(message.finish)) {
      const finish = document.createElement('div')
      finish.className = 'meta finish-warning'
      finish.textContent = `结束原因: ${message.finish}`
      content.appendChild(finish)
    }
    if (message.finish === 'length') {
      const finish = document.createElement('div')
      finish.className = 'meta finish-warning'
      finish.textContent = '输出达到上限，回复可能被截断。'
      content.appendChild(finish)
    }

    if (message.usage) {
      const meta = document.createElement('div')
      meta.className = 'meta'
      const u = message.usage
      const duration = Number(message.duration_ms || 0) / 1000
      meta.textContent = `${message.model || 'unknown'} · in ${u.prompt_tokens || 0} · think ${u.reasoning_tokens || 0} · out ${u.completion_tokens || 0} · cache ${u.cached_tokens || 0} · ${duration.toFixed(1)}s${message.ttft_ms ? ` · 首字 ${message.ttft_ms}ms` : ''}`
      content.appendChild(meta)
    }

    wrapper.appendChild(content)
    return wrapper
  }

  function isNearBottom() {
    const el = els.messagesScroller
    return el.scrollHeight - el.scrollTop - el.clientHeight < 120
  }

  function renderMarkdown(source) {
    const codeBlocks = []
    let text = String(source || '').replace(/```([^\n`]*)\n?([\s\S]*?)```/g, (_, lang, code) => {
      const token = `\u0000CODE${codeBlocks.length}\u0000`
      codeBlocks.push({ lang: String(lang || '').trim(), code: String(code || '') })
      return token
    })
    text = escapeHTML(text)
    const lines = text.split('\n')
    const out = []
    let paragraph = []
    let listType = ''

    const flushParagraph = () => {
      if (!paragraph.length) return
      out.push(`<p>${paragraph.map(inlineMarkdown).join('<br>')}</p>`)
      paragraph = []
    }
    const closeList = () => {
      if (!listType) return
      out.push(`</${listType}>`)
      listType = ''
    }

    for (const line of lines) {
      const codeMatch = line.match(/^\u0000CODE(\d+)\u0000$/)
      if (codeMatch) {
        flushParagraph(); closeList()
        const block = codeBlocks[Number(codeMatch[1])]
        const lang = escapeHTML(block.lang || 'code')
        out.push(`<div class="code-block"><div class="code-head"><span>${lang}</span><button type="button" class="copy-code">复制</button></div><pre><code>${escapeHTML(block.code)}</code></pre></div>`)
        continue
      }
      if (!line.trim()) { flushParagraph(); closeList(); continue }
      const heading = line.match(/^(#{1,3})\s+(.+)$/)
      if (heading) {
        flushParagraph(); closeList()
        const level = heading[1].length
        out.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`)
        continue
      }
      const quote = line.match(/^&gt;\s?(.*)$/)
      if (quote) {
        flushParagraph(); closeList()
        out.push(`<blockquote>${inlineMarkdown(quote[1])}</blockquote>`)
        continue
      }
      const ul = line.match(/^\s*[-*+]\s+(.+)$/)
      const ol = line.match(/^\s*\d+[.)]\s+(.+)$/)
      if (ul || ol) {
        flushParagraph()
        const wanted = ul ? 'ul' : 'ol'
        if (listType && listType !== wanted) closeList()
        if (!listType) { listType = wanted; out.push(`<${wanted}>`) }
        out.push(`<li>${inlineMarkdown((ul || ol)[1])}</li>`)
        continue
      }
      closeList()
      paragraph.push(line)
    }
    flushParagraph(); closeList()
    return out.join('')
  }

  function inlineMarkdown(text) {
    let value = text
    const inlineCodes = []
    value = value.replace(/`([^`]+)`/g, (_, code) => {
      const token = `\u0001INLINE${inlineCodes.length}\u0001`
      inlineCodes.push(code)
      return token
    })
    value = value.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>')
    value = value.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    value = value.replace(/__([^_]+)__/g, '<strong>$1</strong>')
    value = value.replace(/(?<!\*)\*([^*]+)\*(?!\*)/g, '<em>$1</em>')
    value = value.replace(/\u0001INLINE(\d+)\u0001/g, (_, index) => `<code>${inlineCodes[Number(index)]}</code>`)
    return value
  }

  function escapeHTML(value) {
    return String(value).replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char])
  }

  async function connect(url, { silent = false } = {}) {
    if (state.connecting) return false
    state.connecting = true
    state.serverUrl = normalizeURL(url)
    renderConnection()
    els.connectError.classList.add('hidden')
    try {
      const healthController = new AbortController()
      const timer = window.setTimeout(() => healthController.abort(), 5000)
      const health = await fetch(`${state.serverUrl}/api/health`, { signal: healthController.signal, cache: 'no-store' })
      window.clearTimeout(timer)
      if (!health.ok) throw new Error(`健康检查失败 (${health.status})`)
      const models = await fetchJSON('/api/models')
      if (!Array.isArray(models) || models.length === 0) throw new Error('服务端没有可用模型，请检查 config.toml.local')
      state.models = models
      state.connected = true
      localStorage.setItem('wisp_server_url', state.serverUrl)
      renderConnection()
      renderModels()
      await loadSessions()
      hideModal(els.connectModal)
      if (state.currentSessionId && state.sessions.some((item) => item.id === state.currentSessionId)) {
        await openSession(state.currentSessionId, { preserveSelection: true })
      } else {
        newConversation(false)
      }
      return true
    } catch (error) {
      state.connected = false
      state.models = []
      renderConnection()
      const message = errorMessage(error)
      els.connectError.textContent = message
      els.connectError.classList.remove('hidden')
      if (!silent) toast(message, 'error')
      return false
    } finally {
      state.connecting = false
      renderConnection()
    }
  }

  function disconnect() {
    detachLocalStream()
    clearPoll()
    state.connected = false
    state.models = []
    state.sessions = []
    state.messages = []
    state.currentSessionId = ''
    localStorage.removeItem('wisp_current_session_id')
    renderConnection(); renderSessions(); renderMessages(true)
  }

  async function loadSessions() {
    if (!state.connected) return
    const list = await fetchJSON('/api/sessions')
    state.sessions = Array.isArray(list) ? list : []
    if (state.currentSessionId && !state.sessions.some((item) => item.id === state.currentSessionId)) {
      state.currentSessionId = ''
      localStorage.removeItem('wisp_current_session_id')
    }
    renderSessions()
  }

  async function createSession(title) {
    const session = await fetchJSON('/api/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title }),
    })
    state.sessions = [session, ...state.sessions.filter((item) => item.id !== session.id)]
    selectSession(session.id)
    renderSessions()
    return session
  }

  function selectSession(id) {
    state.currentSessionId = id || ''
    if (id) localStorage.setItem('wisp_current_session_id', id)
    else localStorage.removeItem('wisp_current_session_id')
    renderSessions()
  }

  async function openSession(id, { preserveSelection = false } = {}) {
    if (!id || !state.connected) return
    if (!preserveSelection && state.currentSessionId === id && state.messages.length) return
    detachLocalStream()
    clearPoll()
    const token = ++state.viewToken
    selectSession(id)
    state.messages = []
    state.background = false
    setNotice('')
    renderMessages(true)
    renderComposerState()
    try {
      const messages = await fetchJSON(`/api/sessions/${encodeURIComponent(id)}/messages`)
      if (token !== state.viewToken || state.currentSessionId !== id) return
      state.messages = Array.isArray(messages) ? messages.map(normalizeMessage) : []
      renderMessages(true)
      await refreshRunStatus(id, token)
    } catch (error) {
      if (token === state.viewToken) {
        setNotice(errorMessage(error))
        toast(errorMessage(error), 'error')
      }
    }
  }

  function newConversation(clearInput = true) {
    detachLocalStream()
    clearPoll()
    state.viewToken += 1
    selectSession('')
    state.messages = []
    state.background = false
    setNotice('')
    if (clearInput) els.composerInput.value = ''
    autoGrowInput()
    renderMessages(true)
    renderComposerState()
  }

  function normalizeMessage(message) {
    return {
      ...message,
      status: message.finish === 'aborted' ? 'aborted' : (message.finish === 'error' || message.error ? 'error' : 'complete'),
    }
  }

  async function sendMessage() {
    const text = els.composerInput.value.trim()
    if (!text || !state.connected || !state.selectedModel || state.streaming || state.background) return

    let sessionId = state.currentSessionId
    try {
      if (!sessionId) {
        const session = await createSession(text.slice(0, 40))
        sessionId = session.id
      }
    } catch (error) {
      toast(errorMessage(error), 'error')
      return
    }

    const token = state.viewToken
    const userMessage = {
      id: `local-user-${randomID()}`,
      type: 'user', content: text, model: state.selectedModel, thinking: state.selectedThinking,
      created_at: new Date().toISOString(), status: 'complete',
    }
    const assistantMessage = {
      id: `local-assistant-${randomID()}`,
      type: 'assistant', content: '', reasoning: '', model: state.selectedModel,
      thinking: state.selectedThinking, created_at: new Date().toISOString(), status: 'streaming',
    }
    state.messages.push(userMessage, assistantMessage)
    els.composerInput.value = ''
    autoGrowInput()
    state.streaming = true
    state.streamSessionId = sessionId
    state.background = false
    setNotice('')
    state.expandedReasoning.add(assistantMessage.id)
    renderMessages(true)
    renderComposerState()

    const controller = new AbortController()
    state.controller = controller
    const parser = new SSEParser()
    const decoder = new TextDecoder()
    let gotAck = false
    let gotDone = false
    let persisted = true

    const handleEvent = (event, rawData) => {
      const data = safeJSON(rawData) || {}
      switch (event) {
        case 'ack': {
          gotAck = true
          const persistedUser = data.message
          if (persistedUser?.id) userMessage.id = persistedUser.id
          break
        }
        case 'ttft': {
          const ms = Number(data.ms)
          if (Number.isFinite(ms)) assistantMessage.ttft_ms = Math.round(ms)
          break
        }
        case 'reasoning':
          assistantMessage.reasoning = (assistantMessage.reasoning || '') + String(data.text || '')
          break
        case 'delta':
          assistantMessage.content += String(data.text || '')
          if (assistantMessage.content) state.expandedReasoning.delete(assistantMessage.id)
          break
        case 'usage':
          assistantMessage.usage = data
          break
        case 'error':
          assistantMessage.error = String(data.message || '生成失败')
          break
        case 'done': {
          gotDone = true
          persisted = data.persisted !== false
          assistantMessage.finish = String(data.finish || 'stop')
          if (data.message_id) assistantMessage.id = String(data.message_id)
          if (Number.isFinite(Number(data.duration_ms))) assistantMessage.duration_ms = Number(data.duration_ms)
          if (Number.isFinite(Number(data.ttft_ms))) assistantMessage.ttft_ms = Number(data.ttft_ms)
          if (data.error) assistantMessage.error = String(data.error)
          assistantMessage.status = assistantMessage.finish === 'aborted' ? 'aborted' : (assistantMessage.finish === 'error' || assistantMessage.error ? 'error' : 'complete')
          break
        }
      }
      if (token === state.viewToken && state.currentSessionId === sessionId) scheduleRender()
    }

    try {
      const response = await fetch(apiURL(`/api/sessions/${encodeURIComponent(sessionId)}/chat`), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({ message: text, model: state.selectedModel, thinking: state.selectedThinking }),
        signal: controller.signal,
      })
      if (!response.ok) {
        state.messages = state.messages.filter((item) => item !== userMessage && item !== assistantMessage)
        els.composerInput.value = text
        autoGrowInput()
        throw new Error(await readHTTPError(response, `发送失败 (${response.status})`))
      }
      if (!response.body) throw new Error('浏览器没有拿到流式响应体')
      const reader = response.body.getReader()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        const chunk = decoder.decode(value, { stream: true })
        for (const evt of parser.feed(chunk)) handleEvent(evt.event, evt.data)
      }
      for (const evt of parser.feed(decoder.decode())) handleEvent(evt.event, evt.data)
      for (const evt of parser.flush()) handleEvent(evt.event, evt.data)

      if (gotDone && persisted) {
        if (token === state.viewToken && state.currentSessionId === sessionId) {
          await reloadCurrentMessages(sessionId, token)
          await loadSessions().catch(() => undefined)
        }
      } else if (gotAck && !gotDone) {
        if (token === state.viewToken && state.currentSessionId === sessionId) {
          state.background = true
          assistantMessage.status = 'background'
          setNotice('连接中断，服务端会继续生成；完成后会自动刷新。')
          schedulePoll(sessionId, token)
        }
      } else if (!persisted) {
        toast(assistantMessage.error || '回复保存失败', 'error')
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        const message = errorMessage(error)
        if (gotAck) {
          if (token === state.viewToken && state.currentSessionId === sessionId) {
            state.background = true
            assistantMessage.status = 'background'
            assistantMessage.error = ''
            setNotice(`${message}；正在检查服务端生成状态。`)
            schedulePoll(sessionId, token)
          }
        } else {
          // 请求未被服务端确认。保留输入，避免用户内容“消失”。
          if (token === state.viewToken && state.currentSessionId === sessionId) {
            state.messages = state.messages.filter((item) => item !== userMessage && item !== assistantMessage)
            if (!els.composerInput.value) els.composerInput.value = text
            autoGrowInput()
          }
        }
        toast(message, 'error')
      }
    } finally {
      if (state.controller === controller) state.controller = null
      if (token === state.viewToken && state.currentSessionId === sessionId) {
        state.streaming = false
        state.streamSessionId = ''
        if (gotDone) state.background = false
        renderComposerState()
        renderMessages(false)
      }
    }
  }

  async function stopGeneration() {
    const sessionId = state.streamSessionId || state.currentSessionId
    if (!sessionId || !state.connected || (!state.streaming && !state.background)) return
    clearPoll()
    setNotice('正在停止生成…')
    try {
      await fetchJSON(`/api/sessions/${encodeURIComponent(sessionId)}/chat/cancel`, { method: 'POST' })
    } catch (error) {
      toast(errorMessage(error), 'error')
    } finally {
      state.controller?.abort()
      state.controller = null
      state.streaming = false
      state.background = false
      state.streamSessionId = ''
      renderComposerState()
      const token = state.viewToken
      window.setTimeout(async () => {
        if (token !== state.viewToken || state.currentSessionId !== sessionId) return
        await reloadCurrentMessages(sessionId, token).catch(() => undefined)
        setNotice('')
      }, 350)
    }
  }

  async function reloadCurrentMessages(sessionId, token) {
    const messages = await fetchJSON(`/api/sessions/${encodeURIComponent(sessionId)}/messages`)
    if (token !== state.viewToken || state.currentSessionId !== sessionId) return
    state.messages = Array.isArray(messages) ? messages.map(normalizeMessage) : []
    renderMessages(false)
  }

  async function refreshRunStatus(sessionId, token) {
    if (!sessionId || !state.connected) return false
    try {
      const data = await fetchJSON(`/api/sessions/${encodeURIComponent(sessionId)}/chat/status`)
      if (token !== state.viewToken || state.currentSessionId !== sessionId) return Boolean(data?.active)
      state.background = Boolean(data?.active) && !state.streaming
      if (state.background) {
        setNotice('该会话仍在服务端生成。你可以切换会话或刷新页面，生成不会因此被取消。')
        schedulePoll(sessionId, token)
      }
      renderComposerState()
      renderMessages(false)
      return Boolean(data?.active)
    } catch { return false }
  }

  function schedulePoll(sessionId, token) {
    clearPoll()
    state.pollTimer = window.setTimeout(async () => {
      if (token !== state.viewToken || state.currentSessionId !== sessionId) return
      const active = await refreshRunStatus(sessionId, token)
      if (active) return
      state.background = false
      try {
        await reloadCurrentMessages(sessionId, token)
        await loadSessions()
        setNotice('')
      } catch (error) { setNotice(errorMessage(error)) }
      renderComposerState()
    }, 900)
  }

  function clearPoll() {
    if (state.pollTimer != null) {
      window.clearTimeout(state.pollTimer)
      state.pollTimer = null
    }
  }

  function detachLocalStream() {
    state.controller?.abort()
    state.controller = null
    state.streaming = false
    state.streamSessionId = ''
  }

  function autoGrowInput() {
    els.composerInput.style.height = 'auto'
    els.composerInput.style.height = `${Math.min(180, Math.max(24, els.composerInput.scrollHeight))}px`
    renderComposerState()
  }

  function showModal(modal) { modal.classList.remove('hidden') }
  function hideModal(modal) { modal.classList.add('hidden') }
  function errorMessage(error) { return error instanceof Error ? error.message : String(error) }
  function safeJSON(text) { try { return JSON.parse(text) } catch { return null } }
  function randomID() { return globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}` }

  els.connectionButton.addEventListener('click', () => {
    if (!state.connected) {
      els.serverUrlInput.value = state.serverUrl
      showModal(els.connectModal)
      window.setTimeout(() => els.serverUrlInput.focus(), 0)
      return
    }
    const now = Date.now()
    if (now < state.disconnectArmedUntil) {
      state.disconnectArmedUntil = 0
      disconnect()
      toast('已断开连接')
    } else {
      state.disconnectArmedUntil = now + 3000
      toast('再次点击以断开连接')
    }
  })
  els.centerConnectButton.addEventListener('click', () => {
    els.serverUrlInput.value = state.serverUrl
    showModal(els.connectModal)
  })
  els.connectCancel.addEventListener('click', () => hideModal(els.connectModal))
  els.connectSubmit.addEventListener('click', () => { void connect(els.serverUrlInput.value) })
  els.serverUrlInput.addEventListener('keydown', (event) => { if (event.key === 'Enter') void connect(els.serverUrlInput.value) })
  els.connectModal.addEventListener('pointerdown', (event) => { if (event.target === els.connectModal) hideModal(els.connectModal) })

  els.newSessionButton.addEventListener('click', () => newConversation())
  els.modelSelect.addEventListener('change', () => {
    state.selectedModel = els.modelSelect.value
    localStorage.setItem('wisp_selected_model', state.selectedModel)
    syncThinkingForModel()
  })
  els.thinkingSelect.addEventListener('change', () => {
    state.selectedThinking = els.thinkingSelect.value
    state.thinkingByModel[state.selectedModel] = state.selectedThinking
    localStorage.setItem('wisp_thinking_by_model', JSON.stringify(state.thinkingByModel))
  })
  els.composerInput.addEventListener('input', autoGrowInput)
  els.composerInput.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault()
      void sendMessage()
    }
  })
  els.sendButton.addEventListener('click', () => {
    if (state.streaming || state.background) void stopGeneration()
    else void sendMessage()
  })

  els.renameCancel.addEventListener('click', () => hideModal(els.renameModal))
  els.renameSubmit.addEventListener('click', async () => {
    const title = els.renameInput.value.trim()
    if (!title || !state.renameTarget) return
    try {
      await fetchJSON(`/api/sessions/${encodeURIComponent(state.renameTarget)}`, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title }),
      })
      const target = state.sessions.find((item) => item.id === state.renameTarget)
      if (target) target.title = title
      hideModal(els.renameModal)
      renderSessions()
      toast('已重命名')
    } catch (error) { toast(errorMessage(error), 'error') }
  })
  els.renameInput.addEventListener('keydown', (event) => { if (event.key === 'Enter') els.renameSubmit.click() })
  els.renameModal.addEventListener('pointerdown', (event) => { if (event.target === els.renameModal) hideModal(els.renameModal) })

  els.messageList.addEventListener('click', async (event) => {
    const button = event.target.closest('.copy-code')
    if (!button) return
    const code = button.closest('.code-block')?.querySelector('pre code')?.textContent || ''
    try {
      await navigator.clipboard.writeText(code)
      button.textContent = '已复制'
      window.setTimeout(() => { button.textContent = '复制' }, 1200)
    } catch { toast('复制失败，请手动选择代码', 'error') }
  })

  window.addEventListener('online', () => { if (!state.connected) void connect(state.serverUrl, { silent: true }) })
  window.addEventListener('beforeunload', () => state.controller?.abort())

  async function init() {
    renderConnection()
    renderSessions()
    renderMessages(true)
    autoGrowInput()
    els.serverUrlInput.value = state.serverUrl
    const ok = await connect(state.serverUrl, { silent: true })
    if (!ok) showModal(els.connectModal)
    window.setInterval(async () => {
      if (!state.connected) return
      try {
        const response = await fetch(apiURL('/api/health'), { cache: 'no-store' })
        if (!response.ok) throw new Error('offline')
      } catch {
        state.connected = false
        renderConnection()
        els.connectionDot.classList.add('error')
        els.connectionText.textContent = '无响应'
      }
    }, 15000)
  }

  void init()
})()
