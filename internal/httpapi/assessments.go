package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) assessReadiness(w http.ResponseWriter, r *http.Request) {
	var request contracts.AssessReadinessRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.AssessReadiness(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/specimens/"+value.SpecimenID+"/assessments")
	writeJSON(w, http.StatusCreated, value)
}

func (api *API) listAssessments(w http.ResponseWriter, r *http.Request) {
	values, err := api.service.ListAssessmentsForSpecimen(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}
