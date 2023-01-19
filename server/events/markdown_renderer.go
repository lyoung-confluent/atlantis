// Copyright 2017 HootSuite Media Inc.
//
// Licensed under the Apache License, Version 2.0 (the License);
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an AS IS BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Modified hereafter by contributors to runatlantis/atlantis.

package events

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var (
	planCommandTitle            = command.Plan.TitleString()
	applyCommandTitle           = command.Apply.TitleString()
	policyCheckCommandTitle     = command.PolicyCheck.TitleString()
	approvePoliciesCommandTitle = command.ApprovePolicies.TitleString()
	versionCommandTitle         = command.Version.TitleString()
	importCommandTitle          = command.Import.TitleString()
	stateCommandTitle           = command.State.TitleString()
	// maxUnwrappedLines is the maximum number of lines the Terraform output
	// can be before we wrap it in an expandable template.
	maxUnwrappedLines = 12

	//go:embed templates/*
	templatesFS embed.FS
)

// MarkdownRenderer renders responses as markdown.
type MarkdownRenderer struct {
	// gitlabSupportsCommonMark is true if the version of GitLab we're
	// using supports the CommonMark markdown format.
	// If we're not configured with a GitLab client, this will be false.
	gitlabSupportsCommonMark bool
	disableApplyAll          bool
	disableApply             bool
	disableMarkdownFolding   bool
	disableRepoLocking       bool
	enableDiffMarkdownFormat bool
	markdownTemplates        *template.Template
	executableName           string
}

// commonData is data that all responses have.
type commonData struct {
	Command                  string
	SubCommand               string
	Verbose                  bool
	Log                      string
	PlansDeleted             bool
	DisableApplyAll          bool
	DisableApply             bool
	DisableRepoLocking       bool
	EnableDiffMarkdownFormat bool
	ExecutableName           string
}

// errData is data about an error response.
type errData struct {
	Error string
	commonData
}

// failureData is data about a failure response.
type failureData struct {
	Failure string
	commonData
}

// resultData is data about a successful response.
type resultData struct {
	Results []projectResultTmplData
	commonData
}

type planSuccessData struct {
	models.PlanSuccess
	PlanSummary              string
	PlanWasDeleted           bool
	DisableApply             bool
	DisableRepoLocking       bool
	EnableDiffMarkdownFormat bool
}

type policyCheckSuccessData struct {
	models.PolicyCheckSuccess
	PolicyCheckSummary string
}

type projectResultTmplData struct {
	Workspace   string
	RepoRelDir  string
	ProjectName string
	Rendered    string
}

// Initialize templates
func NewMarkdownRenderer(
	gitlabSupportsCommonMark bool,
	disableApplyAll bool,
	disableApply bool,
	disableMarkdownFolding bool,
	disableRepoLocking bool,
	enableDiffMarkdownFormat bool,
	markdownTemplateOverridesDir string,
	executableName string,
) *MarkdownRenderer {
	var templates *template.Template
	templates, _ = template.New("").Funcs(sprig.TxtFuncMap()).ParseFS(templatesFS, "templates/*.tmpl")
	if overrides, err := templates.ParseGlob(fmt.Sprintf("%s/*.tmpl", markdownTemplateOverridesDir)); err == nil {
		// doesn't override if templates directory doesn't exist
		templates = overrides
	}
	return &MarkdownRenderer{
		gitlabSupportsCommonMark: gitlabSupportsCommonMark,
		disableApplyAll:          disableApplyAll,
		disableMarkdownFolding:   disableMarkdownFolding,
		disableApply:             disableApply,
		disableRepoLocking:       disableRepoLocking,
		enableDiffMarkdownFormat: enableDiffMarkdownFormat,
		markdownTemplates:        templates,
		executableName:           executableName,
	}
}

// Render formats the data into a markdown string.
// nolint: interfacer
func (m *MarkdownRenderer) Render(res command.Result, cmdName command.Name, subCmd, log string, verbose bool, vcsHost models.VCSHostType) string {
	commandStr := cases.Title(language.English).String(strings.Replace(cmdName.String(), "_", " ", -1))
	common := commonData{
		Command:                  commandStr,
		SubCommand:               subCmd,
		Verbose:                  verbose,
		Log:                      log,
		PlansDeleted:             res.PlansDeleted,
		DisableApplyAll:          m.disableApplyAll || m.disableApply,
		DisableApply:             m.disableApply,
		DisableRepoLocking:       m.disableRepoLocking,
		EnableDiffMarkdownFormat: m.enableDiffMarkdownFormat,
		ExecutableName:           m.executableName,
	}

	templates := m.markdownTemplates

	if res.Error != nil {
		return m.renderTemplateTrimSpace(templates.Lookup("unwrappedErrWithLog"), errData{res.Error.Error(), common})
	}
	if res.Failure != "" {
		return m.renderTemplateTrimSpace(templates.Lookup("failureWithLog"), failureData{res.Failure, common})
	}
	return m.renderProjectResults(res.ProjectResults, common, vcsHost)
}

func (m *MarkdownRenderer) renderProjectResults(results []command.ProjectResult, common commonData, vcsHost models.VCSHostType) string {
	var resultsTmplData []projectResultTmplData
	numPlanSuccesses := 0
	numPolicyCheckSuccesses := 0
	numVersionSuccesses := 0

	templates := m.markdownTemplates

	for _, result := range results {
		resultData := projectResultTmplData{
			Workspace:   result.Workspace,
			RepoRelDir:  result.RepoRelDir,
			ProjectName: result.ProjectName,
		}
		if result.Error != nil {
			tmpl := templates.Lookup("unwrappedErr")
			if m.shouldUseWrappedTmpl(vcsHost, result.Error.Error()) {
				tmpl = templates.Lookup("wrappedErr")
			}
			resultData.Rendered = m.renderTemplateTrimSpace(tmpl, errData{result.Error.Error(), common})
		} else if result.Failure != "" {
			resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("failure"), failureData{result.Failure, common})
		} else if result.PlanSuccess != nil {
			result.PlanSuccess.TerraformOutput = strings.TrimSpace(result.PlanSuccess.TerraformOutput)
			if m.shouldUseWrappedTmpl(vcsHost, result.PlanSuccess.TerraformOutput) {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("planSuccessWrapped"), planSuccessData{PlanSuccess: *result.PlanSuccess, PlanSummary: result.PlanSuccess.Summary(), PlanWasDeleted: common.PlansDeleted, DisableApply: common.DisableApply, DisableRepoLocking: common.DisableRepoLocking, EnableDiffMarkdownFormat: common.EnableDiffMarkdownFormat})
			} else {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("planSuccessUnwrapped"), planSuccessData{PlanSuccess: *result.PlanSuccess, PlanWasDeleted: common.PlansDeleted, DisableApply: common.DisableApply, DisableRepoLocking: common.DisableRepoLocking, EnableDiffMarkdownFormat: common.EnableDiffMarkdownFormat})
			}
			numPlanSuccesses++
		} else if result.PolicyCheckSuccess != nil {
			result.PolicyCheckSuccess.PolicyCheckOutput = strings.TrimSpace(result.PolicyCheckSuccess.PolicyCheckOutput)
			if m.shouldUseWrappedTmpl(vcsHost, result.PolicyCheckSuccess.PolicyCheckOutput) {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("policyCheckSuccessWrapped"), policyCheckSuccessData{PolicyCheckSuccess: *result.PolicyCheckSuccess, PolicyCheckSummary: result.PolicyCheckSuccess.Summary()})
			} else {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("policyCheckSuccessUnwrapped"), policyCheckSuccessData{PolicyCheckSuccess: *result.PolicyCheckSuccess})
			}
			numPolicyCheckSuccesses++
		} else if result.ApplySuccess != "" {
			output := strings.TrimSpace(result.ApplySuccess)
			if m.shouldUseWrappedTmpl(vcsHost, result.ApplySuccess) {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("applyWrappedSuccess"), struct{ Output string }{output})
			} else {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("applyUnwrappedSuccess"), struct{ Output string }{output})
			}
		} else if result.VersionSuccess != "" {
			output := strings.TrimSpace(result.VersionSuccess)
			if m.shouldUseWrappedTmpl(vcsHost, output) {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("versionWrappedSuccess"), struct{ Output string }{output})
			} else {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("versionUnwrappedSuccess"), struct{ Output string }{output})
			}
			numVersionSuccesses++
		} else if result.ImportSuccess != nil {
			result.ImportSuccess.Output = strings.TrimSpace(result.ImportSuccess.Output)
			if m.shouldUseWrappedTmpl(vcsHost, result.ImportSuccess.Output) {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("importSuccessWrapped"), result.ImportSuccess)
			} else {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("importSuccessUnwrapped"), result.ImportSuccess)
			}
		} else if result.StateRmSuccess != nil {
			result.StateRmSuccess.Output = strings.TrimSpace(result.StateRmSuccess.Output)
			if m.shouldUseWrappedTmpl(vcsHost, result.StateRmSuccess.Output) {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("stateRmSuccessWrapped"), result.StateRmSuccess)
			} else {
				resultData.Rendered = m.renderTemplateTrimSpace(templates.Lookup("stateRmSuccessUnwrapped"), result.StateRmSuccess)
			}
		} else {
			resultData.Rendered = "Found no template. This is a bug!"
		}
		resultsTmplData = append(resultsTmplData, resultData)
	}

	var tmpl *template.Template
	switch {
	case len(resultsTmplData) == 1 && common.Command == planCommandTitle && numPlanSuccesses > 0:
		tmpl = templates.Lookup("singleProjectPlanSuccess")
	case len(resultsTmplData) == 1 && common.Command == planCommandTitle && numPlanSuccesses == 0:
		tmpl = templates.Lookup("singleProjectPlanUnsuccessful")
	case len(resultsTmplData) == 1 && common.Command == policyCheckCommandTitle && numPolicyCheckSuccesses > 0:
		tmpl = templates.Lookup("singleProjectPlanSuccess")
	case len(resultsTmplData) == 1 && common.Command == policyCheckCommandTitle && numPolicyCheckSuccesses == 0:
		tmpl = templates.Lookup("singleProjectPlanUnsuccessful")
	case len(resultsTmplData) == 1 && common.Command == versionCommandTitle && numVersionSuccesses > 0:
		tmpl = templates.Lookup("singleProjectVersionSuccess")
	case len(resultsTmplData) == 1 && common.Command == versionCommandTitle && numVersionSuccesses == 0:
		tmpl = templates.Lookup("singleProjectVersionUnsuccessful")
	case len(resultsTmplData) == 1 && common.Command == applyCommandTitle:
		tmpl = templates.Lookup("singleProjectApply")
	case len(resultsTmplData) == 1 && common.Command == importCommandTitle:
		tmpl = templates.Lookup("singleProjectImport")
	case len(resultsTmplData) == 1 && common.Command == stateCommandTitle:
		switch common.SubCommand {
		case "rm":
			tmpl = templates.Lookup("singleProjectStateRm")
		default:
			return fmt.Sprintf("no template matched–this is a bug: command=%s, subcommand=%s", common.Command, common.SubCommand)
		}
	case common.Command == planCommandTitle,
		common.Command == policyCheckCommandTitle:
		tmpl = templates.Lookup("multiProjectPlan")
	case common.Command == approvePoliciesCommandTitle:
		tmpl = templates.Lookup("approveAllProjects")
	case common.Command == applyCommandTitle:
		tmpl = templates.Lookup("multiProjectApply")
	case common.Command == versionCommandTitle:
		tmpl = templates.Lookup("multiProjectVersion")
	case common.Command == importCommandTitle:
		tmpl = templates.Lookup("multiProjectImport")
	case common.Command == stateCommandTitle:
		switch common.SubCommand {
		case "rm":
			tmpl = templates.Lookup("multiProjectStateRm")
		default:
			return fmt.Sprintf("no template matched–this is a bug: command=%s, subcommand=%s", common.Command, common.SubCommand)
		}
	default:
		return fmt.Sprintf("no template matched–this is a bug: command=%s", common.Command)
	}
	return m.renderTemplateTrimSpace(tmpl, resultData{resultsTmplData, common})
}

// shouldUseWrappedTmpl returns true if we should use the wrapped markdown
// templates that collapse the output to make the comment smaller on initial
// load. Some VCS providers or versions of VCS providers don't support this
// syntax.
func (m *MarkdownRenderer) shouldUseWrappedTmpl(vcsHost models.VCSHostType, output string) bool {
	if m.disableMarkdownFolding {
		return false
	}

	// Bitbucket Cloud and Server don't support the folding markdown syntax.
	if vcsHost == models.BitbucketServer || vcsHost == models.BitbucketCloud {
		return false
	}

	if vcsHost == models.Gitlab && !m.gitlabSupportsCommonMark {
		return false
	}

	return strings.Count(output, "\n") > maxUnwrappedLines
}

func (m *MarkdownRenderer) renderTemplateTrimSpace(tmpl *template.Template, data interface{}) string {
	buf := &bytes.Buffer{}
	if err := tmpl.Execute(buf, data); err != nil {
		return fmt.Sprintf("Failed to render template, this is a bug: %v", err)
	}
	return strings.TrimSpace(buf.String())
}
