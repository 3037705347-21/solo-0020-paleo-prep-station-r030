package domain

import (
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

type AssessmentOutcome string

const (
	AssessmentPassed   AssessmentOutcome = "passed"
	AssessmentReturned AssessmentOutcome = "returned"
)

type ReadinessState string

const (
	ReadinessUnassessed ReadinessState = "unassessed"
	ReadinessPassed     ReadinessState = "passed"
	ReadinessReturned   ReadinessState = "returned"
)

type ReadinessAssessment struct {
	ID         string            `json:"id"`
	SpecimenID string            `json:"specimen_id"`
	Outcome    AssessmentOutcome `json:"outcome"`
	Basis      string            `json:"basis"`
	AssessedBy string            `json:"assessed_by"`
	AssessedAt time.Time         `json:"assessed_at"`
}

func NewReadinessAssessment(
	id string,
	specimen Specimen,
	outcome string,
	basis string,
	assessedBy string,
	now time.Time,
) (ReadinessAssessment, error) {
	if id == "" {
		return ReadinessAssessment{}, contracts.Internal("missing_generated_id", "评估标识未生成", nil)
	}
	if !specimen.CanAssessReadiness() {
		return ReadinessAssessment{}, contracts.Conflict("specimen_not_assessable", "当前标本状态不能提交处置就绪评估")
	}
	decision := AssessmentOutcome(outcome)
	if decision != AssessmentPassed && decision != AssessmentReturned {
		return ReadinessAssessment{}, contracts.Invalid("invalid_assessment_outcome", "评估结论只能是 passed 或 returned")
	}
	basisValue, err := requiredText("评估依据", basis, 500)
	if err != nil {
		return ReadinessAssessment{}, err
	}
	assessor, err := requiredText("评估人", assessedBy, 80)
	if err != nil {
		return ReadinessAssessment{}, err
	}
	return ReadinessAssessment{
		ID:         id,
		SpecimenID: specimen.ID,
		Outcome:    decision,
		Basis:      basisValue,
		AssessedBy: assessor,
		AssessedAt: now.UTC(),
	}, nil
}
