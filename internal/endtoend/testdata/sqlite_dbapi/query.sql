-- name: GetRecord :one
SELECT id, name, note, payload, score, dynamic_value
FROM records
WHERE id = ?;

-- name: GetRecordName :one
SELECT name FROM records WHERE id = ?;

-- name: GetRecordNote :one
SELECT note FROM records WHERE id = ?;

-- name: ListRecords :many
SELECT id, name, note, payload, score, dynamic_value
FROM records
WHERE name = sqlc.arg(name) OR note = sqlc.arg(name)
ORDER BY id;

-- name: ListRecordNotes :many
SELECT note FROM records ORDER BY id;

-- name: InsertRecord :one
INSERT INTO records (name, note, payload, score, dynamic_value)
VALUES (?, ?, ?, ?, ?)
RETURNING id, name, note, payload, score, dynamic_value;

-- name: RenameRecord :exec
UPDATE records SET name = ? WHERE id = ?;

-- name: DeleteRecord :execrows
DELETE FROM records WHERE id = ?;

-- name: DeleteRecordsReturning :execrows
DELETE FROM records
WHERE name = ?
RETURNING id;
