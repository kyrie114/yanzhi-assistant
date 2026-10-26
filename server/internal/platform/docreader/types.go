package docreader

// Block is a parsed document block, independent of ingestion and chunk packing.
type Block struct {
	Kind string
	Text string
}

// Page carries the original physical page number and parsed text blocks.
type Page struct {
	PageNo int
	Blocks []Block
}
