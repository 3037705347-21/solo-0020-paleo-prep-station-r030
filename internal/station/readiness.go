package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// SubmitReadinessAssessment 在当前标本上提交一次处置就绪评估。
// 评估事实写入与标本版本推进在同一个仓储变更内提交，避免二者脱节。
func (s *Service) SubmitReadinessAssessment(
	ctx context.Context,
	specimenID string,
	request contracts.ReadinessAssessmentRequest,
) (domain.ReadinessAssessment, error) {
	if err := ctx.Err(); err != nil {
		return domain.ReadinessAssessment{}, err
	}

	var created domain.ReadinessAssessment
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		specimen, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}
		latest, hasLatest := m.LatestReadinessAssessment(specimenID)
		var latestRef *domain.ReadinessAssessment
		if hasLatest {
			latestRef = &latest
		}
		assessment, err := domain.NewReadinessAssessment(
			s.nextID("readiness"),
			specimen,
			latestRef,
			request.Decision,
			request.Basis,
			request.AssessedBy,
			s.now(),
		)
		if err != nil {
			return err
		}
		specimen.TouchReadinessVersion(s.now())
		m.PutReadinessAssessment(assessment)
		m.PutSpecimen(specimen)
		created = assessment
		return nil
	})
	return created, err
}

// ListReadinessAssessments 返回某标本按提交顺序排列的全部评估记录。
func (s *Service) ListReadinessAssessments(
	ctx context.Context,
	specimenID string,
) ([]domain.ReadinessAssessment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.ReadinessAssessment
	err := s.repo.Read(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		values = m.ReadinessAssessmentsForSpecimen(specimenID)
		return nil
	})
	return values, err
}
