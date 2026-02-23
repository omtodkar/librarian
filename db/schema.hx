N::Document {
    INDEX file_path: String,
    title: String,
    doc_type: String,
    summary: String,
    headings: String,
    frontmatter: String,
    content_hash: String,
    chunk_count: U32,
    indexed_at: Date DEFAULT NOW,
}

V::DocChunk {
    file_path: String,
    section_heading: String,
    section_hierarchy: String,
    chunk_index: U32,
    content: String,
    token_count: U32,
}

N::CodeFile {
    INDEX file_path: String,
    language: String,
    last_referenced_at: Date DEFAULT NOW,
}

E::HasChunk {
    From: Document,
    To: DocChunk,
    Properties: {}
}

E::References {
    From: Document,
    To: CodeFile,
    Properties: {
        context: String,
    }
}

E::RelatedDoc {
    From: Document,
    To: Document,
    Properties: {
        relation_type: String,
    }
}
