package imwrap

import (
	"testing"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/stretchr/testify/require"
)

func sampleBatch() proto.QuestionRequest {
	return proto.QuestionRequest{
		ID:        "batch-1",
		SessionID: "s1",
		Questions: []proto.QuestionItem{
			{
				ID:       "q1",
				Type:     "single_choice",
				Question: "Which database?",
				Choices: []proto.QuestionChoice{
					{ID: "pg", Label: "PostgreSQL"},
					{ID: "mongo", Label: "MongoDB"},
				},
			},
		},
	}
}

func TestBuildQuestionAnswerSingleChoiceByIndex(t *testing.T) {
	t.Parallel()
	ans, err := BuildQuestionAnswer(sampleBatch(), "2")
	require.NoError(t, err)
	require.Equal(t, "batch-1", ans.BatchRequestID)
	require.Len(t, ans.Responses, 1)
	require.Equal(t, "q1", ans.Responses[0].QuestionID)
	require.Equal(t, []string{"mongo"}, ans.Responses[0].SelectedIDs)
}

func TestBuildQuestionAnswerByLabelAndPrefix(t *testing.T) {
	t.Parallel()
	ans, err := BuildQuestionAnswer(sampleBatch(), "postgres")
	require.NoError(t, err)
	require.Equal(t, []string{"pg"}, ans.Responses[0].SelectedIDs)
}

func TestBuildQuestionAnswerMultiChoice(t *testing.T) {
	t.Parallel()
	req := sampleBatch()
	req.Questions[0].Type = "multi_choice"
	ans, err := BuildQuestionAnswer(req, "1, mongo")
	require.NoError(t, err)
	require.Equal(t, []string{"pg", "mongo"}, ans.Responses[0].SelectedIDs)
}

func TestBuildQuestionAnswerBadChoice(t *testing.T) {
	t.Parallel()
	_, err := BuildQuestionAnswer(sampleBatch(), "sqlite")
	require.ErrorContains(t, err, "没有匹配")
	_, err = BuildQuestionAnswer(sampleBatch(), "9")
	require.ErrorContains(t, err, "超出范围")
}

func TestBuildQuestionAnswerYesNo(t *testing.T) {
	t.Parallel()
	req := sampleBatch()
	req.Questions[0] = proto.QuestionItem{ID: "q1", Type: "yes_no", Question: "Proceed?"}

	ans, err := BuildQuestionAnswer(req, "yes")
	require.NoError(t, err)
	require.NotNil(t, ans.Responses[0].Yes)
	require.True(t, *ans.Responses[0].Yes)

	ans, err = BuildQuestionAnswer(req, "否")
	require.NoError(t, err)
	require.NotNil(t, ans.Responses[0].Yes)
	require.False(t, *ans.Responses[0].Yes)

	_, err = BuildQuestionAnswer(req, "maybe")
	require.ErrorContains(t, err, "yes 或 no")
}

func TestBuildQuestionAnswerFreeText(t *testing.T) {
	t.Parallel()
	req := sampleBatch()
	req.Questions[0] = proto.QuestionItem{ID: "q1", Type: "free_text", Question: "Name?"}
	ans, err := BuildQuestionAnswer(req, "  my name  ")
	require.NoError(t, err)
	require.Equal(t, "my name", ans.Responses[0].FillInText)
}

func TestBuildQuestionAnswerBatchNumberedLines(t *testing.T) {
	t.Parallel()
	req := proto.QuestionRequest{
		ID: "b",
		Questions: []proto.QuestionItem{
			{ID: "q1", Type: "free_text", Question: "a?"},
			{ID: "q2", Type: "yes_no", Question: "b?"},
		},
	}
	ans, err := BuildQuestionAnswer(req, "1: hello\n2) no")
	require.NoError(t, err)
	require.Equal(t, "hello", ans.Responses[0].FillInText)
	require.NotNil(t, ans.Responses[1].Yes)
	require.False(t, *ans.Responses[1].Yes)
}

func TestBuildQuestionAnswerEmpty(t *testing.T) {
	t.Parallel()
	_, err := BuildQuestionAnswer(sampleBatch(), "  ")
	require.ErrorContains(t, err, "回复为空")
}

func TestFormatQuestionText(t *testing.T) {
	t.Parallel()
	text := FormatQuestionText(sampleBatch())
	require.Contains(t, text, "❓")
	require.Contains(t, text, "Which database?")
	require.Contains(t, text, "1. PostgreSQL")
	require.Contains(t, text, "2. MongoDB")
	require.Contains(t, text, "回复序号")

	yesNo := sampleBatch()
	yesNo.Questions[0] = proto.QuestionItem{ID: "q1", Type: "yes_no", Question: "Proceed?", Label: "确认"}
	text = FormatQuestionText(yesNo)
	require.Contains(t, text, "[确认] Proceed?")
	require.Contains(t, text, "yes 或 no")
}
