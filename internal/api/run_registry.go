package api

import agentruntime "wisp/internal/runtime"

type RunRegistry = agentruntime.RunRegistry

func NewRunRegistry() *RunRegistry { return agentruntime.NewRunRegistry() }
