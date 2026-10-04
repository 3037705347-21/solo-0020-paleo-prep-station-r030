package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) submitReadinessAssessment(w http.ResponseWriter, r *http.Request) {
	var request contracts.ReadinessAssessmentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.SubmitReadinessAssessment(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func (api *API) listReadinessAssessments(w http.ResponseWriter, r *http.Request) {
	values, err := api.service.ListReadinessAssessments(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}
