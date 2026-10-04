package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) AssessReadiness(
	ctx context.Context,
	specimenID string,
	request contracts.AssessReadinessRequest,
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
		assessment, err := domain.NewReadinessAssessment(
			s.nextID("assessment"),
			specimen,
			request.Outcome,
			request.Basis,
			request.AssessedBy,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := specimen.ApplyAssessment(assessment.Outcome, s.now()); err != nil {
			return err
		}
		m.PutAssessment(assessment)
		m.PutSpecimen(specimen)
		created = assessment
		return nil
	})
	return created, err
}

func (s *Service) ListAssessmentsForSpecimen(
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
		values = m.ListAssessmentsForSpecimen(specimenID)
		return nil
	})
	return values, err
}
