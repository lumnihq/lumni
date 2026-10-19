package protocol

const (
	ReasonActorPaused      ReasonCode = "ACTOR_PAUSED"
	ReasonConcurrencyLimit ReasonCode = "CONCURRENCY_LIMIT"
	ReasonRateLimited      ReasonCode = "RATE_LIMITED"
	ReasonBudgetPacing     ReasonCode = "BUDGET_PACING"
)

type ActorCost struct {
	ActorID       string `json:"actor_id"`
	CostMicrosUSD int64  `json:"cost_micros_usd"`
	Actions       int64  `json:"actions"`
}
