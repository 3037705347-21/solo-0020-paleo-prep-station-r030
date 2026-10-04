package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type specimenView struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	Status        string `json:"status"`
	Readiness     string `json:"readiness"`
	Version       int    `json:"version"`
}

type assessmentView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Outcome    string `json:"outcome"`
}

type planView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Status     string `json:"status"`
}

type planSnapshot struct {
	Plan     planView `json:"plan"`
	Progress []struct {
		Percent int `json:"percent"`
	} `json:"progress"`
}

type releaseView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to check")
	flag.Parse()

	if err := run(*workflow); err != nil {
		fmt.Fprintln(os.Stderr, "workflow check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("workflow check passed: %s\n", *workflow)
}

func run(workflow string) error {
	handler := httpapi.NewRouter(station.New(repository.NewMemory()))
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	switch workflow {
	case "register-specimen":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if specimen.Status != "registered" {
			return fmt.Errorf("expected registered status, got %s", specimen.Status)
		}
		if specimen.Readiness != "unassessed" {
			return fmt.Errorf("expected unassessed readiness, got %s", specimen.Readiness)
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "assess-readiness":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if err := expectPlanRejected(client, server.URL, specimen.ID); err != nil {
			return fmt.Errorf("unassessed specimen must not accept plans: %w", err)
		}
		var missing assessmentView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/specimen-missing/assessments",
			contracts.AssessReadinessRequest{Outcome: "passed", Basis: "基质稳固", AssessedBy: "lead-wang"},
			http.StatusNotFound,
			&missing,
		); err != nil {
			return err
		}
		returned, err := submitAssessment(client, server.URL, specimen.ID, "returned")
		if err != nil {
			return err
		}
		if returned.Outcome != "returned" || returned.SpecimenID != specimen.ID {
			return errors.New("returned assessment was not recorded against the specimen")
		}
		afterReturn, err := fetchSpecimen(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		if afterReturn.Readiness != "returned" || afterReturn.Version != specimen.Version+1 {
			return fmt.Errorf("expected returned readiness with version %d, got %s@%d", specimen.Version+1, afterReturn.Readiness, afterReturn.Version)
		}
		if err := expectPlanRejected(client, server.URL, specimen.ID); err != nil {
			return fmt.Errorf("returned specimen must not accept plans: %w", err)
		}
		passed, err := submitAssessment(client, server.URL, specimen.ID, "passed")
		if err != nil {
			return err
		}
		if passed.Outcome != "passed" {
			return fmt.Errorf("expected passed reassessment, got %s", passed.Outcome)
		}
		afterPass, err := fetchSpecimen(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		if afterPass.Readiness != "passed" || afterPass.Version != afterReturn.Version+1 {
			return fmt.Errorf("expected passed readiness with version %d, got %s@%d", afterReturn.Version+1, afterPass.Readiness, afterPass.Version)
		}
		var duplicate assessmentView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/assessments",
			contracts.AssessReadinessRequest{Outcome: "passed", Basis: "重复提交", AssessedBy: "lead-wang"},
			http.StatusConflict,
			&duplicate,
		); err != nil {
			return fmt.Errorf("duplicate assessment must be rejected: %w", err)
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return fmt.Errorf("passed specimen must accept plans: %w", err)
		}
		if plan.SpecimenID != specimen.ID {
			return errors.New("plan references the wrong specimen")
		}
		var history struct {
			Items []assessmentView `json:"items"`
		}
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID+"/assessments", nil, http.StatusOK, &history); err != nil {
			return err
		}
		if len(history.Items) != 2 || history.Items[0].Outcome != "returned" || history.Items[1].Outcome != "passed" {
			return errors.New("assessment history must keep the returned and passed decisions in order")
		}
		return nil

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if _, err := submitAssessment(client, server.URL, specimen.ID, "passed"); err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		var approved planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&approved,
		); err != nil {
			return err
		}
		if approved.Status != "approved" {
			return fmt.Errorf("expected approved plan, got %s", approved.Status)
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "log-preparation":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "矩阵剥离"); err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "completed" || len(snapshot.Progress) != 2 {
			return errors.New("plan did not complete with exactly two progress entries")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "release-for-study":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var release releaseView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{
				Purpose:    "比较解剖研究",
				Recipient:  "地层研究组",
				ApprovedBy: "keeper-zhou",
			},
			http.StatusCreated,
			&release,
		); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		return nil
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: "PPS-2026-001",
			FieldLabel:    "北坡-07",
			TaxonHint:     "大型脊椎动物",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克边角完好",
		},
		http.StatusCreated,
		&specimen,
	)
	return specimen, err
}

func createPlan(client *http.Client, baseURL, specimenID string) (planView, error) {
	var plan planView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusCreated,
		&plan,
	)
	return plan, err
}

func submitAssessment(client *http.Client, baseURL, specimenID, outcome string) (assessmentView, error) {
	var assessment assessmentView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/assessments",
		contracts.AssessReadinessRequest{
			Outcome:    outcome,
			Basis:      "现场加固完成，基质附着稳定",
			AssessedBy: "lead-wang",
		},
		http.StatusCreated,
		&assessment,
	)
	return assessment, err
}

func fetchSpecimen(client *http.Client, baseURL, specimenID string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(client, http.MethodGet, baseURL+"/specimens/"+specimenID, nil, http.StatusOK, &specimen)
	return specimen, err
}

func expectPlanRejected(client *http.Client, baseURL, specimenID string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusConflict,
		&ignored,
	)
}

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	specimen, err := registerSpecimen(client, baseURL)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	if _, err := submitAssessment(client, baseURL, specimen.ID, "passed"); err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func recordProgress(client *http.Client, baseURL, planID string, percent int, stage string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{
			Stage:      stage,
			Percent:    percent,
			Note:       "本地链路检查",
			RecordedBy: "preparator-chen",
		},
		http.StatusCreated,
		&ignored,
	)
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return nil
}
