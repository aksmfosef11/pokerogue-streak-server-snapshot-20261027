package defs

type EggProofOptions struct {
	SourceType            int   `json:"sourceType"`
	Pulled                bool  `json:"pulled"`
	Species               int   `json:"species,omitempty"`
	Tier                  *int  `json:"tier,omitempty"`
	Timestamp             int   `json:"timestamp"`
	IsShiny               *bool `json:"isShiny,omitempty"`
	VariantTier           *int  `json:"variantTier,omitempty"`
	EggMoveIndex          *int  `json:"eggMoveIndex,omitempty"`
	HatchWaves            *int  `json:"hatchWaves,omitempty"`
	OverrideHiddenAbility bool  `json:"overrideHiddenAbility,omitempty"`
}
type EggProofStock struct {
	VariantTier *int `json:"variantTier,omitempty"`
	ID          int  `json:"id"`
	Species     int  `json:"species"`
}
type EggProofContext struct {
	StarterPity map[int][]int   `json:"starterPity,omitempty"`
	CaughtAttrs map[int]string  `json:"caughtAttrs,omitempty"`
	EggPity     []int           `json:"eggPity"`
	UnlockPity  []int           `json:"unlockPity"`
	Caught      []int           `json:"caught"`
	Eggs        []EggProofStock `json:"eggs"`
}
type EggProofBatch struct {
	ID    string `json:"id"`
	Size  int    `json:"size"`
	Index int    `json:"index"`
}
type EggProof struct {
	ID       string          `json:"id"`
	TicketID string          `json:"ticketId,omitempty"`
	Sequence int             `json:"sequence,omitempty"`
	Seed     string          `json:"seed,omitempty"` // Only used for unverified local fallback.
	Options  EggProofOptions `json:"options"`
	Context  EggProofContext `json:"context"`
	Batch    *EggProofBatch  `json:"batch,omitempty"`
	Egg      EggData         `json:"egg"`
}
