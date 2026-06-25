package problemtrace

const (
	AttrAgentStepIndex = "agent.step.index"
	AttrAgentPhase     = "agent.phase"

	AttrModelProvider     = "model.provider"
	AttrModelName         = "model.name"
	AttrModelInputTokens  = "model.input_tokens"
	AttrModelOutputTokens = "model.output_tokens"

	AttrToolName     = "tool.name"
	AttrToolArgsHash = "tool.args_hash"
	AttrToolExitCode = "tool.exit_code"
	AttrToolTimedOut = "tool.timed_out"

	AttrRepoPath     = "repo.path"
	AttrRepoLanguage = "repo.language"
	AttrRepoChanged  = "repo.changed_files"

	AttrDirectionID         = "direction.id"
	AttrDirectionStatus     = "direction.status"
	AttrDirectionConfidence = "direction.confidence"

	AttrFrontierPriority = "frontier.priority"

	AttrMemoryCardID      = "memory.card_id"
	AttrMemoryUsageStatus = "memory.usage_status"
	AttrMemorySimilarity  = "memory.similarity"

	AttrPromptSnapshotID = "prompt.snapshot_id"
	AttrPromptBlockKind  = "prompt.block.kind"

	AttrTestCommand        = "test.command"
	AttrTestStatus         = "test.status"
	AttrTestErrorSignature = "test.error_signature"

	AttrErrorSignature = "error.signature"
	AttrEventType      = "event.type"
)

const (
	SpanProblemRun        = "problem.run"
	SpanPromptBuild       = "prompt.build"
	SpanModelCall         = "model.call"
	SpanToolCall          = "tool.call"
	SpanDirectionEvaluate = "direction.evaluate"
	SpanFrontierUpdate    = "frontier.update"
	SpanMemoryRetrieve    = "memory.retrieve"
	SpanMemoryInject      = "memory.inject"
	SpanPatchApply        = "patch.apply"
	SpanTestRun           = "test.run"
	SpanVerificationRun   = "verification.run"
)

const (
	LinkSuggests    = "suggests"
	LinkInjects     = "injects"
	LinkCaused      = "caused"
	LinkSupports    = "supports"
	LinkRefutes     = "refutes"
	LinkProduces    = "produces"
	LinkVerifiedBy  = "verified_by"
	LinkSimilarTo   = "similar_to"
	LinkDerivedFrom = "derived_from"
)
