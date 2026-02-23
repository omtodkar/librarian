// Document CRUD

QUERY add_document(file_path: String, title: String, doc_type: String, summary: String, headings: String, frontmatter: String, content_hash: String, chunk_count: U32) =>
    doc <- AddN<Document>({file_path: file_path, title: title, doc_type: doc_type, summary: summary, headings: headings, frontmatter: frontmatter, content_hash: content_hash, chunk_count: chunk_count})
    RETURN doc

QUERY get_document_by_path(file_path: String) =>
    doc <- N<Document>({file_path: file_path})
    RETURN doc

QUERY list_documents() =>
    docs <- N<Document>
    RETURN docs

QUERY delete_document(doc_id: ID) =>
    DROP N<Document>(doc_id)::OutE<HasChunk>
    DROP N<Document>(doc_id)::Out<HasChunk>
    DROP N<Document>(doc_id)::OutE<References>
    DROP N<Document>(doc_id)::OutE<RelatedDoc>
    DROP N<Document>(doc_id)::InE<RelatedDoc>
    DROP N<Document>(doc_id)
    RETURN "deleted"

// DocChunk operations

QUERY add_chunk(vector: [F64], content: String, file_path: String, section_heading: String, section_hierarchy: String, chunk_index: U32, token_count: U32, doc_id: ID) =>
    chunk <- AddV<DocChunk>(vector, {file_path: file_path, section_heading: section_heading, section_hierarchy: section_hierarchy, chunk_index: chunk_index, content: content, token_count: token_count})
    doc <- N<Document>(doc_id)
    AddE<HasChunk>::From(doc)::To(chunk)
    RETURN chunk

QUERY search_chunks(vector: [F64], limit: I64) =>
    chunks <- SearchV<DocChunk>(vector, limit)
    RETURN chunks

QUERY get_chunks_for_document(doc_id: ID) =>
    chunks <- N<Document>(doc_id)::Out<HasChunk>
    RETURN chunks

// CodeFile operations

QUERY add_code_file(file_path: String, language: String) =>
    code_file <- AddN<CodeFile>({file_path: file_path, language: language})
    RETURN code_file

QUERY get_code_file_by_path(file_path: String) =>
    code_file <- N<CodeFile>({file_path: file_path})
    RETURN code_file

QUERY get_referenced_code_files(doc_id: ID) =>
    code_files <- N<Document>(doc_id)::Out<References>
    RETURN code_files

// Reference edges

QUERY add_reference(doc_id: ID, code_file_id: ID, context: String) =>
    doc <- N<Document>(doc_id)
    code_file <- N<CodeFile>(code_file_id)
    reference <- AddE<References>({context: context})::From(doc)::To(code_file)
    RETURN reference

// Related document edges

QUERY add_related_doc(from_doc_id: ID, to_doc_id: ID, relation_type: String) =>
    from_doc <- N<Document>(from_doc_id)
    to_doc <- N<Document>(to_doc_id)
    rel <- AddE<RelatedDoc>({relation_type: relation_type})::From(from_doc)::To(to_doc)
    RETURN rel

QUERY get_related_documents(doc_id: ID) =>
    related <- N<Document>(doc_id)::Out<RelatedDoc>
    RETURN related
