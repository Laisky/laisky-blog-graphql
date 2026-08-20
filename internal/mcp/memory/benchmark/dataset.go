package benchmark

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

var sessionKeyPattern = regexp.MustCompile(`^session_(\d+)$`)

// LoadDataset loads canonical JSONL or a supported public benchmark export.
func LoadDataset(path, format string) (Dataset, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Dataset{}, errors.Wrapf(err, "stat benchmark dataset %s", path)
	}

	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "auto"
	}
	if info.IsDir() {
		if format != "auto" && format != "beam" {
			return Dataset{}, errors.Errorf("dataset directory requires format beam, got %q", format)
		}
		dataset, raw, loadErr := loadBEAM(path)
		if loadErr != nil {
			return Dataset{}, loadErr
		}
		dataset.SHA256 = hashBytes(raw)
		return normalizeDataset(dataset)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return Dataset{}, errors.Wrapf(err, "read benchmark dataset %s", path)
	}
	if format == "auto" {
		format, err = detectDatasetFormat(path, raw)
		if err != nil {
			return Dataset{}, err
		}
	}

	var dataset Dataset
	switch format {
	case "canonical", "jsonl":
		dataset, err = parseCanonicalJSONL(raw)
	case "longmemeval":
		dataset, err = parseLongMemEval(raw)
	case "locomo":
		dataset, err = parseLoCoMo(raw)
	case "memoryagentbench", "mab":
		dataset, err = parseMemoryAgentBench(raw)
	default:
		return Dataset{}, errors.Errorf("unsupported benchmark format %q", format)
	}
	if err != nil {
		return Dataset{}, err
	}
	dataset.SHA256 = hashBytes(raw)
	return normalizeDataset(dataset)
}

func detectDatasetFormat(path string, raw []byte) (string, error) {
	if strings.EqualFold(filepath.Ext(path), ".jsonl") {
		line := firstDataLine(raw)
		var probe map[string]json.RawMessage
		if json.Unmarshal(line, &probe) == nil {
			if _, ok := probe["type"]; ok {
				return "canonical", nil
			}
			if _, ok := probe["questions"]; ok {
				return "memoryagentbench", nil
			}
		}
		return "canonical", nil
	}

	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &objects); err != nil || len(objects) == 0 {
		return "", errors.New("cannot auto-detect dataset format; pass --format explicitly")
	}
	first := objects[0]
	if _, ok := first["question_id"]; ok {
		return "longmemeval", nil
	}
	if _, ok := first["sample_id"]; ok {
		if _, hasConversation := first["conversation"]; hasConversation {
			return "locomo", nil
		}
	}
	if _, ok := first["questions"]; ok {
		return "memoryagentbench", nil
	}
	return "", errors.New("cannot auto-detect dataset format; pass --format explicitly")
}

func firstDataLine(raw []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || bytes.HasPrefix(line, []byte("#")) {
			continue
		}
		return append([]byte(nil), line...)
	}
	return nil
}

type canonicalRow struct {
	Type            string            `json:"type"`
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Source          string            `json:"source"`
	ID              string            `json:"id"`
	Path            string            `json:"path"`
	Content         string            `json:"content"`
	ContentEncoding string            `json:"content_encoding"`
	Category        string            `json:"category"`
	SessionID       string            `json:"session_id"`
	Timestamp       string            `json:"timestamp"`
	Metadata        map[string]string `json:"metadata"`
	Query           string            `json:"query"`
	PathPrefix      string            `json:"path_prefix"`
	GoldPaths       []string          `json:"gold_paths"`
	GoldEvidence    []string          `json:"gold_evidence"`
	Answer          string            `json:"answer"`
	Rubric          []string          `json:"rubric"`
	Unanswerable    bool              `json:"unanswerable"`
}

func parseCanonicalJSONL(raw []byte) (Dataset, error) {
	dataset := Dataset{Name: "canonical-memory-benchmark", Version: "v1", Source: "canonical-jsonl"}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || bytes.HasPrefix(line, []byte("#")) {
			continue
		}
		var row canonicalRow
		if err := json.Unmarshal(line, &row); err != nil {
			return Dataset{}, errors.Wrapf(err, "decode canonical dataset line %d", lineNo)
		}
		switch strings.ToLower(strings.TrimSpace(row.Type)) {
		case "dataset":
			if row.Name != "" {
				dataset.Name = row.Name
			}
			if row.Version != "" {
				dataset.Version = row.Version
			}
			if row.Source != "" {
				dataset.Source = row.Source
			}
		case "document":
			dataset.Documents = append(dataset.Documents, Document{
				ID: row.ID, Path: row.Path, Content: row.Content, ContentEncoding: row.ContentEncoding,
				Category: row.Category, SessionID: row.SessionID, Timestamp: row.Timestamp, Metadata: row.Metadata,
			})
		case "query":
			dataset.Queries = append(dataset.Queries, Query{
				ID: row.ID, Text: row.Query, PathPrefix: row.PathPrefix, GoldPaths: row.GoldPaths,
				GoldEvidence: row.GoldEvidence, Answer: row.Answer, Rubric: row.Rubric,
				Category: row.Category, Unanswerable: row.Unanswerable, Metadata: row.Metadata,
			})
		default:
			return Dataset{}, errors.Errorf("canonical dataset line %d has unsupported type %q", lineNo, row.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return Dataset{}, errors.Wrap(err, "scan canonical dataset")
	}
	return dataset, nil
}

type longMemEvalTurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer"`
}

type longMemEvalItem struct {
	QuestionID         string              `json:"question_id"`
	QuestionType       string              `json:"question_type"`
	Question           string              `json:"question"`
	Answer             string              `json:"answer"`
	QuestionDate       string              `json:"question_date"`
	HaystackSessionIDs []string            `json:"haystack_session_ids"`
	HaystackDates      []string            `json:"haystack_dates"`
	HaystackSessions   [][]longMemEvalTurn `json:"haystack_sessions"`
	AnswerSessionIDs   []string            `json:"answer_session_ids"`
}

func parseLongMemEval(raw []byte) (Dataset, error) {
	var items []longMemEvalItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return Dataset{}, errors.Wrap(err, "decode LongMemEval dataset")
	}
	dataset := Dataset{Name: "LongMemEval", Version: "cleaned", Source: "xiaowu0162/LongMemEval"}
	for itemIndex, item := range items {
		questionID := strings.TrimSpace(item.QuestionID)
		if questionID == "" {
			questionID = fmt.Sprintf("question-%d", itemIndex+1)
		}
		prefix := "/longmemeval/" + safeSegment(questionID) + "/"
		pathsBySession := make(map[string]string, len(item.HaystackSessions))
		goldEvidence := make([]string, 0)
		for sessionIndex, turns := range item.HaystackSessions {
			sessionID := valueAt(item.HaystackSessionIDs, sessionIndex)
			if sessionID == "" {
				sessionID = fmt.Sprintf("session-%d", sessionIndex+1)
			}
			path := prefix + fmt.Sprintf("%04d-%s.md", sessionIndex+1, safeSegment(sessionID))
			pathsBySession[sessionID] = path
			date := valueAt(item.HaystackDates, sessionIndex)
			var body strings.Builder
			fmt.Fprintf(&body, "# Session %s\n\n", sessionID)
			if date != "" {
				fmt.Fprintf(&body, "Date: %s\n\n", date)
			}
			for _, turn := range turns {
				fmt.Fprintf(&body, "**%s:** %s\n\n", strings.ToLower(turn.Role), turn.Content)
				if turn.HasAnswer && strings.TrimSpace(turn.Content) != "" {
					goldEvidence = append(goldEvidence, turn.Content)
				}
			}
			dataset.Documents = append(dataset.Documents, Document{
				ID: questionID + ":" + sessionID, Path: path, Content: body.String(),
				Category: item.QuestionType, SessionID: sessionID, Timestamp: date,
			})
		}
		goldPaths := make([]string, 0, len(item.AnswerSessionIDs))
		for _, sessionID := range item.AnswerSessionIDs {
			if path := pathsBySession[sessionID]; path != "" {
				goldPaths = append(goldPaths, path)
			}
		}
		unanswerable := strings.HasSuffix(strings.ToLower(questionID), "_abs") || strings.EqualFold(item.QuestionType, "abstention")
		category := item.QuestionType
		if unanswerable {
			category = "abstention"
		}
		dataset.Queries = append(dataset.Queries, Query{
			ID: questionID, Text: item.Question, PathPrefix: prefix, GoldPaths: uniqueStrings(goldPaths),
			GoldEvidence: uniqueStrings(goldEvidence), Answer: item.Answer, Category: category,
			Unanswerable: unanswerable, Metadata: map[string]string{"question_date": item.QuestionDate},
		})
	}
	return dataset, nil
}

type locomoTurn struct {
	Speaker     string `json:"speaker"`
	DiaID       any    `json:"dia_id"`
	Text        string `json:"text"`
	BLIPCaption string `json:"blip_caption"`
}

type locomoQA struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Category any    `json:"category"`
	Evidence []any  `json:"evidence"`
}

type locomoSample struct {
	SampleID     string                     `json:"sample_id"`
	Conversation map[string]json.RawMessage `json:"conversation"`
	QA           []locomoQA                 `json:"qa"`
}

func parseLoCoMo(raw []byte) (Dataset, error) {
	var samples []locomoSample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return Dataset{}, errors.Wrap(err, "decode LoCoMo dataset")
	}
	dataset := Dataset{Name: "LoCoMo", Version: "locomo10", Source: "snap-research/locomo"}
	for sampleIndex, sample := range samples {
		sampleID := strings.TrimSpace(sample.SampleID)
		if sampleID == "" {
			sampleID = fmt.Sprintf("conversation-%d", sampleIndex+1)
		}
		prefix := "/locomo/" + safeSegment(sampleID) + "/"
		sessionKeys := sortedSessionKeys(sample.Conversation)
		pathByDialog := make(map[string]string)
		textByDialog := make(map[string]string)
		for _, sessionKey := range sessionKeys {
			var turns []locomoTurn
			if err := json.Unmarshal(sample.Conversation[sessionKey], &turns); err != nil {
				return Dataset{}, errors.Wrapf(err, "decode LoCoMo %s %s", sampleID, sessionKey)
			}
			dateKey := sessionKey + "_date_time"
			var date string
			_ = json.Unmarshal(sample.Conversation[dateKey], &date)
			path := prefix + sessionKey + ".md"
			var body strings.Builder
			fmt.Fprintf(&body, "# %s\n\n", sessionKey)
			if date != "" {
				fmt.Fprintf(&body, "Date: %s\n\n", date)
			}
			for _, turn := range turns {
				dialogID := anyString(turn.DiaID)
				text := strings.TrimSpace(turn.Text)
				if text == "" {
					text = strings.TrimSpace(turn.BLIPCaption)
				}
				fmt.Fprintf(&body, "[%s] **%s:** %s\n\n", dialogID, turn.Speaker, text)
				if dialogID != "" {
					pathByDialog[dialogID] = path
					textByDialog[dialogID] = text
				}
			}
			dataset.Documents = append(dataset.Documents, Document{
				ID: sampleID + ":" + sessionKey, Path: path, Content: body.String(),
				Category: "conversation-session", SessionID: sessionKey, Timestamp: date,
			})
		}
		for queryIndex, qa := range sample.QA {
			goldPaths := make([]string, 0, len(qa.Evidence))
			goldEvidence := make([]string, 0, len(qa.Evidence))
			for _, evidenceID := range qa.Evidence {
				key := anyString(evidenceID)
				if path := pathByDialog[key]; path != "" {
					goldPaths = append(goldPaths, path)
				}
				if text := textByDialog[key]; text != "" {
					goldEvidence = append(goldEvidence, text)
				}
			}
			category := anyString(qa.Category)
			if category == "" {
				category = "qa"
			}
			dataset.Queries = append(dataset.Queries, Query{
				ID: fmt.Sprintf("%s:q-%04d", sampleID, queryIndex+1), Text: qa.Question,
				PathPrefix: prefix, GoldPaths: uniqueStrings(goldPaths), GoldEvidence: uniqueStrings(goldEvidence),
				Answer: qa.Answer, Category: category,
			})
		}
	}
	return dataset, nil
}

func parseMemoryAgentBench(raw []byte) (Dataset, error) {
	objects, err := decodeJSONObjectSequence(raw)
	if err != nil {
		return Dataset{}, errors.Wrap(err, "decode MemoryAgentBench export")
	}
	dataset := Dataset{Name: "MemoryAgentBench", Version: "ICLR-2026", Source: "ai-hyz/MemoryAgentBench"}
	for sampleIndex, object := range objects {
		prefix := fmt.Sprintf("/memoryagentbench/sample-%05d/", sampleIndex+1)
		texts := extractMemoryTexts(object)
		if len(texts) == 0 {
			return Dataset{}, errors.Errorf("MemoryAgentBench sample %d has no supported context field", sampleIndex+1)
		}
		goldPaths := make([]string, 0, len(texts))
		for textIndex, text := range texts {
			path := prefix + fmt.Sprintf("context-%04d.md", textIndex+1)
			goldPaths = append(goldPaths, path)
			dataset.Documents = append(dataset.Documents, Document{
				ID: fmt.Sprintf("mab:%d:%d", sampleIndex+1, textIndex+1), Path: path,
				Content: text, Category: "incremental-memory",
			})
		}
		questions := rawStringSlice(object["questions"])
		answers := rawStringSlice(object["answers"])
		metadata := rawObject(object["metadata"])
		questionTypes := anyStringSlice(metadata["question_types"])
		questionIDs := anyStringSlice(metadata["question_ids"])
		qaPairIDs := anyStringSlice(metadata["qa_pair_ids"])
		for questionIndex, question := range questions {
			queryID := valueAt(qaPairIDs, questionIndex)
			if queryID == "" {
				queryID = valueAt(questionIDs, questionIndex)
			}
			if queryID == "" {
				queryID = fmt.Sprintf("mab:%05d:q-%04d", sampleIndex+1, questionIndex+1)
			}
			category := valueAt(questionTypes, questionIndex)
			if category == "" {
				category = anyString(metadata["source"])
			}
			if category == "" {
				category = "memoryagentbench"
			}
			dataset.Queries = append(dataset.Queries, Query{
				ID: queryID, Text: question, PathPrefix: prefix, GoldPaths: append([]string(nil), goldPaths...),
				Answer: valueAt(answers, questionIndex), Category: category,
				Metadata: map[string]string{"label_granularity": "sample-level"},
			})
		}
	}
	return dataset, nil
}

func loadBEAM(dir string) (Dataset, []byte, error) {
	chatPath := filepath.Join(dir, "chat.json")
	questionsPath := filepath.Join(dir, "probing_questions", "probing_questions.json")
	chatRaw, err := os.ReadFile(chatPath)
	if err != nil {
		return Dataset{}, nil, errors.Wrap(err, "read BEAM chat.json")
	}
	questionsRaw, err := os.ReadFile(questionsPath)
	if err != nil {
		return Dataset{}, nil, errors.Wrap(err, "read BEAM probing questions")
	}
	var batches []struct {
		BatchNumber int          `json:"batch_number"`
		Turns       [][]beamTurn `json:"turns"`
	}
	if err := json.Unmarshal(chatRaw, &batches); err != nil {
		return Dataset{}, nil, errors.Wrap(err, "decode BEAM chat")
	}
	dataset := Dataset{Name: "BEAM", Version: filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir), Source: "mohammadtavakoli78/BEAM"}
	pathByID := make(map[string]string)
	textByID := make(map[string]string)
	for _, batch := range batches {
		for conversationIndex, turns := range batch.Turns {
			path := fmt.Sprintf("/beam/batch-%04d/conversation-%04d.md", batch.BatchNumber, conversationIndex+1)
			var body strings.Builder
			fmt.Fprintf(&body, "# Batch %d conversation %d\n\n", batch.BatchNumber, conversationIndex+1)
			for _, turn := range turns {
				id := anyString(turn.ID)
				fmt.Fprintf(&body, "[%s] **%s:** %s\n\n", id, turn.Role, turn.Content)
				if id != "" {
					pathByID[id] = path
					textByID[id] = turn.Content
				}
			}
			dataset.Documents = append(dataset.Documents, Document{
				ID: fmt.Sprintf("beam:%d:%d", batch.BatchNumber, conversationIndex+1), Path: path,
				Content: body.String(), Category: "conversation",
			})
		}
	}
	var groups map[string][]map[string]any
	if err := json.Unmarshal(questionsRaw, &groups); err != nil {
		return Dataset{}, nil, errors.Wrap(err, "decode BEAM probing questions")
	}
	categories := make([]string, 0, len(groups))
	for category := range groups {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	queryNumber := 0
	for _, category := range categories {
		for _, item := range groups[category] {
			queryNumber++
			question := anyString(item["question"])
			answer := firstNonEmpty(anyString(item["answer"]), anyString(item["ideal_answer"]), anyString(item["ideal_response"]))
			evidenceIDs := collectScalarStrings(item["source_chat_ids"])
			goldPaths := make([]string, 0, len(evidenceIDs))
			goldEvidence := make([]string, 0, len(evidenceIDs))
			for _, id := range evidenceIDs {
				if path := pathByID[id]; path != "" {
					goldPaths = append(goldPaths, path)
				}
				if text := textByID[id]; text != "" {
					goldEvidence = append(goldEvidence, text)
				}
			}
			rubric := anyStringSlice(item["rubric"])
			dataset.Queries = append(dataset.Queries, Query{
				ID: fmt.Sprintf("beam:q-%05d", queryNumber), Text: question, PathPrefix: "/beam/",
				GoldPaths: uniqueStrings(goldPaths), GoldEvidence: uniqueStrings(goldEvidence), Answer: answer,
				Rubric: rubric, Category: category, Unanswerable: category == "abstention",
			})
		}
	}
	raw := make([]byte, 0, len(chatRaw)+len(questionsRaw)+1)
	raw = append(raw, chatRaw...)
	raw = append(raw, '\n')
	raw = append(raw, questionsRaw...)
	return dataset, raw, nil
}

type beamTurn struct {
	Role    string `json:"role"`
	ID      any    `json:"id"`
	Content string `json:"content"`
}

func normalizeDataset(dataset Dataset) (Dataset, error) {
	if strings.TrimSpace(dataset.Name) == "" {
		dataset.Name = "memory-benchmark"
	}
	if strings.TrimSpace(dataset.Version) == "" {
		dataset.Version = time.Now().UTC().Format("2006-01-02")
	}
	if len(dataset.Documents) == 0 {
		return Dataset{}, errors.New("benchmark dataset has no documents")
	}
	if len(dataset.Queries) == 0 {
		return Dataset{}, errors.New("benchmark dataset has no queries")
	}
	documentPaths := make(map[string]struct{}, len(dataset.Documents))
	for index := range dataset.Documents {
		document := &dataset.Documents[index]
		document.Path = strings.TrimSpace(document.Path)
		if document.Path == "" {
			return Dataset{}, errors.Errorf("document %d has an empty path", index+1)
		}
		if _, exists := documentPaths[document.Path]; exists {
			return Dataset{}, errors.Errorf("duplicate document path %q", document.Path)
		}
		documentPaths[document.Path] = struct{}{}
		if document.ID == "" {
			document.ID = document.Path
		}
		if document.ContentEncoding == "" {
			document.ContentEncoding = "utf-8"
		}
	}
	queryIDs := make(map[string]struct{}, len(dataset.Queries))
	for index := range dataset.Queries {
		query := &dataset.Queries[index]
		query.ID = strings.TrimSpace(query.ID)
		query.Text = strings.TrimSpace(query.Text)
		if query.ID == "" || query.Text == "" {
			return Dataset{}, errors.Errorf("query %d requires id and query text", index+1)
		}
		if _, exists := queryIDs[query.ID]; exists {
			return Dataset{}, errors.Errorf("duplicate query id %q", query.ID)
		}
		queryIDs[query.ID] = struct{}{}
		query.GoldPaths = uniqueStrings(query.GoldPaths)
		query.GoldEvidence = uniqueStrings(query.GoldEvidence)
		query.Rubric = uniqueStrings(query.Rubric)
		if !query.Unanswerable && len(query.GoldPaths) == 0 && len(query.GoldEvidence) == 0 {
			return Dataset{}, errors.Errorf("answerable query %q requires gold_paths or gold_evidence", query.ID)
		}
		for _, path := range query.GoldPaths {
			if _, exists := documentPaths[path]; !exists {
				return Dataset{}, errors.Errorf("query %q references unknown gold path %q", query.ID, path)
			}
		}
	}
	return dataset, nil
}

func sortedSessionKeys(conversation map[string]json.RawMessage) []string {
	type sessionKey struct {
		name  string
		index int
	}
	keys := make([]sessionKey, 0)
	for key := range conversation {
		match := sessionKeyPattern.FindStringSubmatch(key)
		if len(match) != 2 {
			continue
		}
		index, _ := strconv.Atoi(match[1])
		keys = append(keys, sessionKey{name: key, index: index})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].index < keys[j].index })
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key.name)
	}
	return result
}

func decodeJSONObjectSequence(raw []byte) ([]map[string]json.RawMessage, error) {
	var array []map[string]json.RawMessage
	if json.Unmarshal(raw, &array) == nil {
		return array, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || bytes.HasPrefix(line, []byte("#")) {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(line, &object); err != nil {
			return nil, errors.Wrap(err, "decode JSONL object")
		}
		array = append(array, object)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "scan JSONL objects")
	}
	return array, nil
}

func extractMemoryTexts(object map[string]json.RawMessage) []string {
	for _, key := range []string{"context", "memory", "text", "content", "input"} {
		if text := rawString(object[key]); text != "" {
			return []string{text}
		}
	}
	for _, key := range []string{"contexts", "chunks", "documents", "memories"} {
		if values := rawStringSlice(object[key]); len(values) > 0 {
			return values
		}
	}
	return nil
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return ""
}

func rawStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var stringsValue []string
	if json.Unmarshal(raw, &stringsValue) == nil {
		return stringsValue
	}
	var single string
	if json.Unmarshal(raw, &single) == nil && single != "" {
		return []string{single}
	}
	var values []any
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text := anyString(value); text != "" {
			result = append(result, text)
			continue
		}
		if object, ok := value.(map[string]any); ok {
			text := firstNonEmpty(anyString(object["content"]), anyString(object["text"]), anyString(object["value"]))
			if text != "" {
				result = append(result, text)
			}
		}
	}
	return result
}

func rawObject(raw json.RawMessage) map[string]any {
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	return object
}

func anyStringSlice(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := anyString(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		if text := anyString(value); text != "" {
			return []string{text}
		}
		return nil
	}
}

func anyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func collectScalarStrings(value any) []string {
	result := make([]string, 0)
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				visit(typed[key])
			}
		case []any:
			for _, item := range typed {
				visit(item)
			}
		default:
			if text := anyString(current); text != "" {
				result = append(result, text)
			}
		}
	}
	visit(value)
	return uniqueStrings(result)
}

func safeSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "item"
	}
	return result
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
