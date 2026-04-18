package embedder

import (
	"bytes"
	"drag/pkg/extractor"
	_ "embed"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

//go:embed models/model.onnx
var modelBytes []byte

//go:embed models/tokenizer.json
var tokenizerBytes []byte

type ONNXEmbedder struct {
	session   *ort.DynamicAdvancedSession
	tokenizer *tokenizer.Tokenizer
}

func NewONNXEmbedder() (*ONNXEmbedder, error) {
	// The onnxruntime.dll file should be placed in the root directory of the project
	// This looks for the DLL in the directory where the app was started.
	dllPath := filepath.Join("D:\\Naveen\\drag\\drag", "onnxruntime.dll")
	ort.SetSharedLibraryPath(dllPath)
// 	execPath, err := os.Executable()
// if err != nil {
//     return nil, fmt.Errorf("failed to get executable path: %w", err)
// }
// 	execDir := filepath.Dir(execPath)
// 	dllPath := filepath.Join(execDir, "onnxruntime.dll")
// 	ort.SetSharedLibraryPath(dllPath)
	
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("failed to init onnx environment: %w", err)
	}

	// NewDynamicAdvancedSession needs a file path, not bytes
	// Therefore, write embedded bytes to a temp file
	tmpModel, err := os.CreateTemp("", "model-*.onnx")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp model file: %w", err)
	}
	defer os.Remove(tmpModel.Name())
	if _, err := tmpModel.Write(modelBytes); err != nil {
		return nil, fmt.Errorf("failed to write model bytes: %w", err)
	}
	tmpModel.Close()

	// DynamicAdvancedSession — tensors specified at run time, not init time
	session, err := ort.NewDynamicAdvancedSession(
		tmpModel.Name(), // onnxFilePath
		[]string{"input_ids", "attention_mask", "token_type_ids"}, // input names
		[]string{"last_hidden_state"}, // output names
		nil, // <-- Passed options instead of nil
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load onnx session: %w", err)
	}

	// FromReader accepts an io.Reader — works with embedded bytes
	tk, err := pretrained.FromReader(bytes.NewReader(tokenizerBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to load tokenizer: %w", err)
	}

	return &ONNXEmbedder{
		session:   session,
		tokenizer: tk,
	}, nil
}

func (o *ONNXEmbedder) EmbedText(text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}

	// EncodeSingle takes an optional bool for special tokens
	en, err := o.tokenizer.EncodeSingle(text, true) // true = add [CLS] and [SEP]
	if err != nil {
		return nil, fmt.Errorf("tokenization failed: %w", err)
	}

	ids := en.GetIds()
	seqLen := len(ids)

	inputIDs := make([]int64, seqLen)
	attentionMask := make([]int64, seqLen)
	tokenTypeIDs := make([]int64, seqLen)
	for i, id := range ids {
		inputIDs[i] = int64(id)
		attentionMask[i] = 1
		tokenTypeIDs[i] = 0
	}

	shape := ort.NewShape(1, int64(seqLen))

	tensorIDs, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to create input_ids tensor: %w", err)
	}
	defer tensorIDs.Destroy()

	tensorMask, err := ort.NewTensor(shape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("failed to create attention_mask tensor: %w", err)
	}
	defer tensorMask.Destroy()

	tensorType, err := ort.NewTensor(shape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to create token_type_ids tensor: %w", err)
	}
	defer tensorType.Destroy()

	// Output shape: [1, seqLen, 384]
	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(seqLen), 384))
	if err != nil {
		return nil, fmt.Errorf("failed to create output tensor: %w", err)
	}
	defer outputTensor.Destroy()

	// Run takes (inputs, outputs)
	err = o.session.Run(
		[]ort.Value{tensorIDs, tensorMask, tensorType},
		[]ort.Value{outputTensor},
	)
	if err != nil {
		return nil, fmt.Errorf("inference failed: %w", err)
	}

	outputData := outputTensor.GetData()
	embedding := meanPooling(outputData, seqLen)
	return normalize(embedding), nil
}

func (o *ONNXEmbedder) Embed(texts []extractor.Chunk) ([][]float32, error) {
	batch := make([][]float32, len(texts))
	for i, t := range texts {
		vec, err := o.EmbedText(t.Content)
		if err != nil {
			return nil, err
		}
		batch[i] = vec
	}
	return batch, nil
}

func (o *ONNXEmbedder) Destroy() {
	o.session.Destroy()
	ort.DestroyEnvironment()
}

func meanPooling(flat []float32, seqLen int) []float32 {
	result := make([]float32, 384)
	for i := 0; i < seqLen; i++ {
		for j := 0; j < 384; j++ {
			result[j] += flat[i*384+j]
		}
	}
	for j := range result {
		result[j] /= float32(seqLen)
	}
	return result
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := float32(math.Sqrt(sum))
	result := make([]float32, len(v))
	for i, x := range v {
		result[i] = x / norm
	}
	return result
}