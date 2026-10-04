package domain

import (
	"strings"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

// ReadinessDecision 是处置就绪评估的结论。
type ReadinessDecision string

const (
	// ReadinessApproved 表示评估通过，标本准入制备方案创建。
	ReadinessApproved ReadinessDecision = "approved"
	// ReadinessReturned 表示评估退回，标本在再次评估通过前不能创建新方案。
	ReadinessReturned ReadinessDecision = "returned"
)

// ReadinessAssessment 记录一次独立的标本处置就绪评估事实。
// SpecimenVersion 锚定提交评估时的标本版本，使评估事实与标本版本不会互相脱节。
type ReadinessAssessment struct {
	ID              string            `json:"id"`
	SpecimenID      string            `json:"specimen_id"`
	SpecimenVersion int               `json:"specimen_version"`
	Decision        ReadinessDecision `json:"decision"`
	Basis           string            `json:"basis"`
	AssessedBy      string            `json:"assessed_by"`
	AssessedAt      time.Time         `json:"assessed_at"`
}

// ParseReadinessDecision 把公共请求中的结论文本解析为稳定的评估结论。
func ParseReadinessDecision(value string) (ReadinessDecision, error) {
	switch ReadinessDecision(strings.TrimSpace(value)) {
	case ReadinessApproved:
		return ReadinessApproved, nil
	case ReadinessReturned:
		return ReadinessReturned, nil
	default:
		return "", contracts.Invalid("invalid_readiness_decision", "评估结论只能是 approved 或 returned")
	}
}

// NewReadinessAssessment 在标本当前版本上构造一条评估记录。
// latest 为该标本最新一次评估时，结论相同的重复提交会被明确拒绝。
func NewReadinessAssessment(
	id string,
	specimen Specimen,
	latest *ReadinessAssessment,
	decision string,
	basis string,
	assessedBy string,
	now time.Time,
) (ReadinessAssessment, error) {
	if id == "" {
		return ReadinessAssessment{}, contracts.Internal("missing_generated_id", "评估标识未生成", nil)
	}
	if !specimen.CanAssessReadiness() {
		return ReadinessAssessment{}, contracts.Conflict(
			"specimen_not_assessable",
			"只有已登记或已规划标本可以提交处置就绪评估",
		)
	}
	conclusion, err := ParseReadinessDecision(decision)
	if err != nil {
		return ReadinessAssessment{}, err
	}
	basisValue, err := requiredText("评估依据", basis, 500)
	if err != nil {
		return ReadinessAssessment{}, err
	}
	assessor, err := requiredText("评估人", assessedBy, 80)
	if err != nil {
		return ReadinessAssessment{}, err
	}
	if latest != nil && latest.Decision == conclusion {
		return ReadinessAssessment{}, contracts.Conflict(
			"readiness_decision_duplicate",
			"评估结论与最新一次评估相同，不能重复评估",
		)
	}
	return ReadinessAssessment{
		ID:              id,
		SpecimenID:      specimen.ID,
		SpecimenVersion: specimen.Version,
		Decision:        conclusion,
		Basis:           basisValue,
		AssessedBy:      assessor,
		AssessedAt:      now.UTC(),
	}, nil
}

// IsApproved 表示该评估结论是否允许创建新的修复方案。
func (a ReadinessAssessment) IsApproved() bool {
	return a.Decision == ReadinessApproved
}
