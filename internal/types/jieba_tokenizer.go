package types

// JiebaTokenizer is the narrow segmentation surface used by retrieval and metrics.
type JiebaTokenizer interface {
	Cut(sentence string, hmm bool) []string
	CutForSearch(sentence string, hmm bool) []string
}

// Jieba is a global instance of the configured Chinese text segmentation tool.
var Jieba JiebaTokenizer = newJieba()
