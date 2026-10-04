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
	"sync"
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
	Version       int    `json:"version"`
}

type planView struct {
	ID                    string `json:"id"`
	SpecimenID            string `json:"specimen_id"`
	Status                string `json:"status"`
	ReadinessAssessmentID string `json:"readiness_assessment_id"`
}

type readinessView struct {
	ID              string `json:"id"`
	SpecimenID      string `json:"specimen_id"`
	SpecimenVersion int    `json:"specimen_version"`
	Decision        string `json:"decision"`
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

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if _, err := assessReadiness(client, server.URL, specimen.ID, "approved"); err != nil {
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

	case "assess-readiness":
		return checkReadinessWorkflow(client, server.URL)
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

// checkReadinessWorkflow 覆盖处置就绪评估的准入、退回、重复、未知标本、状态冲突与并发决定边界。
func checkReadinessWorkflow(client *http.Client, baseURL string) error {
	// 未知标本：提交与查询评估都必须明确返回 404。
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/specimen-unknown/readiness-assessments",
		contracts.ReadinessAssessmentRequest{
			Decision: "approved", Basis: "依据充分", AssessedBy: "lead-prep-wang",
		}, http.StatusNotFound, "specimen_not_found",
	); err != nil {
		return err
	}
	if _, err := requestExpectError(
		client, http.MethodGet, baseURL+"/specimens/specimen-unknown/readiness-assessments",
		nil, http.StatusNotFound, "specimen_not_found",
	); err != nil {
		return err
	}

	// 未评估标本不能创建方案。
	first, err := registerSpecimen(client, baseURL)
	if err != nil {
		return err
	}
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/"+first.ID+"/plans",
		contracts.CreatePlanRequest{
			Objective: "暴露主要关节面", Method: "手工显微剥离", TargetExposure: 80,
		}, http.StatusConflict, "readiness_not_assessed",
	); err != nil {
		return err
	}

	// 非法结论和缺失字段返回 400。
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/"+first.ID+"/readiness-assessments",
		contracts.ReadinessAssessmentRequest{
			Decision: "maybe", Basis: "依据充分", AssessedBy: "lead-prep-wang",
		}, http.StatusBadRequest, "invalid_readiness_decision",
	); err != nil {
		return err
	}
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/"+first.ID+"/readiness-assessments",
		contracts.ReadinessAssessmentRequest{
			Decision: "approved", Basis: "", AssessedBy: "lead-prep-wang",
		}, http.StatusBadRequest, "required_field",
	); err != nil {
		return err
	}

	// 通过评估后方案创建准入，方案必须回链该评估事实。
	assessment, err := assessReadiness(client, baseURL, first.ID, "approved")
	if err != nil {
		return err
	}
	if assessment.SpecimenVersion != 1 {
		return fmt.Errorf("assessment must anchor specimen version 1, got %d", assessment.SpecimenVersion)
	}
	var firstAfter specimenView
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens/"+first.ID, nil, http.StatusOK, &firstAfter); err != nil {
		return err
	}
	// 评估事实提交后标本版本被推进，二者在同一事务内生效。
	if firstAfter.Version != 2 {
		return fmt.Errorf("specimen version must advance to 2 after assessment, got %d", firstAfter.Version)
	}
	plan, err := createPlan(client, baseURL, first.ID)
	if err != nil {
		return err
	}
	if plan.ReadinessAssessmentID != assessment.ID {
		return errors.New("created plan is not linked to the approving readiness assessment")
	}

	// 重复相同结论必须被拒绝。
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/"+first.ID+"/readiness-assessments",
		contracts.ReadinessAssessmentRequest{
			Decision: "approved", Basis: "再次确认", AssessedBy: "lead-prep-wang",
		}, http.StatusConflict, "readiness_decision_duplicate",
	); err != nil {
		return err
	}

	// 退回 -> 阻断创建；重复退回 -> 冲突；再次评估通过 -> 恢复准入。
	returned, err := registerSpecimenAs(client, baseURL, "PPS-2026-002")
	if err != nil {
		return err
	}
	if _, err := assessReadiness(client, baseURL, returned.ID, "returned"); err != nil {
		return err
	}
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/"+returned.ID+"/plans",
		contracts.CreatePlanRequest{
			Objective: "暴露主要关节面", Method: "手工显微剥离", TargetExposure: 80,
		}, http.StatusConflict, "readiness_not_approved",
	); err != nil {
		return err
	}
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/"+returned.ID+"/readiness-assessments",
		contracts.ReadinessAssessmentRequest{
			Decision: "returned", Basis: "仍然松动", AssessedBy: "lead-prep-wang",
		}, http.StatusConflict, "readiness_decision_duplicate",
	); err != nil {
		return err
	}
	reapproved, err := assessReadiness(client, baseURL, returned.ID, "approved")
	if err != nil {
		return err
	}
	replan, err := createPlan(client, baseURL, returned.ID)
	if err != nil {
		return err
	}
	if replan.ReadinessAssessmentID != reapproved.ID {
		return errors.New("re-admitted plan must link to the latest approving assessment")
	}

	// 并发提交相同结论：恰好一次成功，其余全部冲突，且之后只准入一次方案创建。
	parallel, err := registerSpecimenAs(client, baseURL, "PPS-2026-003")
	if err != nil {
		return err
	}
	const workers = 16
	statuses := make(chan int, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, postErr := postReadinessStatus(client, baseURL, parallel.ID, "approved")
			if postErr != nil {
				status = 0
			}
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	accepted, conflicts := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			accepted++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("concurrent assessment returned unexpected status %d", status)
		}
	}
	if accepted != 1 || conflicts != workers-1 {
		return fmt.Errorf("expected exactly 1 accepted and %d conflicts, got %d and %d", workers-1, accepted, conflicts)
	}
	if _, err := createPlan(client, baseURL, parallel.ID); err != nil {
		return fmt.Errorf("plan creation after concurrent decision failed: %w", err)
	}

	// 已进入制备后的标本不能再提交评估。
	late, latePlan, err := completedPlanSetupAs(client, baseURL, "PPS-2026-004")
	if err != nil {
		return err
	}
	if err := recordProgress(client, baseURL, latePlan.ID, 100, "表面加固"); err != nil {
		return err
	}
	if _, err := requestExpectError(
		client, http.MethodPost, baseURL+"/specimens/"+late.ID+"/readiness-assessments",
		contracts.ReadinessAssessmentRequest{
			Decision: "approved", Basis: "补充评估", AssessedBy: "lead-prep-wang",
		}, http.StatusConflict, "specimen_not_assessable",
	); err != nil {
		return err
	}

	// 评估历史完整可读，顺序为通过、退回、再通过。
	var history struct {
		Items []readinessView `json:"items"`
	}
	if err := requestJSON(
		client, http.MethodGet, baseURL+"/specimens/"+returned.ID+"/readiness-assessments",
		nil, http.StatusOK, &history,
	); err != nil {
		return err
	}
	if len(history.Items) != 2 {
		return fmt.Errorf("expected 2 assessments on returned specimen, got %d", len(history.Items))
	}
	if history.Items[0].Decision != "returned" || history.Items[1].Decision != "approved" {
		return fmt.Errorf("assessment history order mismatch: %s then %s", history.Items[0].Decision, history.Items[1].Decision)
	}
	return nil
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	return registerSpecimenAs(client, baseURL, "PPS-2026-001")
}

func registerSpecimenAs(client *http.Client, baseURL, catalog string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: catalog,
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

func assessReadiness(client *http.Client, baseURL, specimenID, decision string) (readinessView, error) {
	var assessment readinessView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/readiness-assessments",
		contracts.ReadinessAssessmentRequest{
			Decision:   decision,
			Basis:      "基质已固结，夹克完整，无松动碎块",
			AssessedBy: "lead-prep-wang",
		},
		http.StatusCreated,
		&assessment,
	)
	return assessment, err
}

func postReadinessStatus(client *http.Client, baseURL, specimenID, decision string) (int, error) {
	encoded, err := json.Marshal(contracts.ReadinessAssessmentRequest{
		Decision:   decision,
		Basis:      "并发评估",
		AssessedBy: "lead-prep-wang",
	})
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/specimens/"+specimenID+"/readiness-assessments", bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, nil
}

// requestExpectError 断言响应状态码和稳定错误码，用于冲突与非法输入边界。
func requestExpectError(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	wantCode string,
) (errorBody, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errorBody{}, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return errorBody{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return errorBody{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return errorBody{}, err
	}
	if response.StatusCode != wantStatus {
		return errorBody{}, fmt.Errorf("request %s %s returned %d (want %d): %s", method, url, response.StatusCode, wantStatus, string(raw))
	}
	var decoded errorBody
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errorBody{}, fmt.Errorf("decode error body for %s %s: %w", method, url, err)
	}
	if decoded.Code != wantCode {
		return errorBody{}, fmt.Errorf("request %s %s returned error code %q, want %q", method, url, decoded.Code, wantCode)
	}
	return decoded, nil
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

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	return completedPlanSetupAs(client, baseURL, "PPS-2026-001")
}

func completedPlanSetupAs(client *http.Client, baseURL, catalog string) (specimenView, planView, error) {
	specimen, err := registerSpecimenAs(client, baseURL, catalog)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	if _, err := assessReadiness(client, baseURL, specimen.ID, "approved"); err != nil {
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
