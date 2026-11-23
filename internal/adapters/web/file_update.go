package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/response"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

var (
	errFileUpdateField    = errors.New("invalid file update field")
	errFileUpdateTrailing = errors.New("data after the JSON value")
)

// UpdateFile handles PUT /files/{id}. The JSON body can change ttl (seconds, or null to remove
// the expiry), privacy and burn_after_download; an omitted field keeps its value. The edit code
// comes only from the X-Edit-Code header, as for Delete. The response is the GET /files/{id} body.
func (h *FileHandler) UpdateFile(w http.ResponseWriter, r *http.Request) {
	var body fileUpdateJSON

	decoder := json.NewDecoder(r.Body)
	// An unknown field is an error, so that a client does not send edit_code in the body and
	// think that it was used.
	decoder.DisallowUnknownFields()

	err := decoder.Decode(&body)
	if err == nil {
		err = requireJSONEnd(decoder)
	}

	if err != nil {
		h.writeDecodeError(w, r, err)

		return
	}

	opts, err := h.updateOptions(body)
	if err != nil {
		h.writeUpdateError(w, r, err)

		return
	}

	file, err := h.manager.Update(r.Context(), r.PathValue("id"), editCodeHeader(r), h.reader(r), opts)
	if err != nil {
		h.writeUpdateError(w, r, err)

		return
	}

	h.writeJSON(w, r, http.StatusOK, h.toJSON(r, file))
}

// updateOptions converts the PUT body into files.UpdateOptions. It checks the values before
// the usecase call, so that an invalid request does not read the record.
func (h *FileHandler) updateOptions(body fileUpdateJSON) (files.UpdateOptions, error) {
	opts := files.UpdateOptions{
		ExpiresAt: nil, Privacy: nil, BurnAfterDownload: body.BurnAfterDownload, ClearExpiry: false,
	}

	switch {
	case len(body.TTL) == 0:
		// An omitted ttl keeps the current expiry.
	case string(body.TTL) == "null":
		opts.ClearExpiry = true
	default:
		var ttl int64

		err := json.Unmarshal(body.TTL, &ttl)
		if err != nil || ttl < 0 || ttl > maxTTLSeconds {
			return opts, fmt.Errorf("%w: ttl must be null or 0..%d seconds", errFileUpdateField, maxTTLSeconds)
		}

		expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Second)
		opts.ExpiresAt = &expiresAt
	}

	if body.Privacy != nil {
		privacy := domain.Privacy(*body.Privacy)
		if !privacy.Valid() {
			return opts, domain.ErrInvalidPrivacy
		}

		opts.Privacy = &privacy
	}

	err := requirePrivacySupport(h.accounts, opts.Privacy)
	if err != nil {
		return opts, err
	}

	return opts, nil
}

// requireJSONEnd makes sure that only white space follows the JSON value. Without this check
// the decoder ignores the data after the value, and the client does not know that the server
// did not use it.
func requireJSONEnd(decoder *json.Decoder) error {
	_, err := decoder.Token()
	if errors.Is(err, io.EOF) {
		return nil
	}

	return errFileUpdateTrailing
}

func (h *FileHandler) writeUpdateError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errFileUpdateField) {
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusUnprocessableEntity, err.Error())

		return
	}

	if status, _ := domainErrStatus(err); status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "update file failed", "err", err)
	}

	h.writeMutationError(w, r, err)
}

// fileUpdateJSON is the body of PUT /files/{id}. A nil field keeps the current value. TTL stays
// raw, because an omitted ttl and a null ttl have different meanings. The decoder stays by hand:
// it refuses unknown fields (a client that sends edit_code in the body must know that the server
// does not use it), and the generated ogenapi.UpdateFileRequest decoder skips them.
type fileUpdateJSON struct {
	Privacy           *string         `json:"privacy"`
	BurnAfterDownload *bool           `json:"burn_after_download"`
	TTL               json.RawMessage `json:"ttl"`
}
