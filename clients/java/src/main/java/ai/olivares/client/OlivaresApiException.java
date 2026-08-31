// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

/**
 * The API's single error envelope ({@code {"error":{code,message}}}) plus
 * transport context. Unchecked so the generated operations keep clean
 * signatures (no {@code throws} clutter) — catch it explicitly where you handle
 * failures. {@link #getCode()} values (e.g. {@code "not_found"}, {@code "forbidden"},
 * {@code "rate_limited"}) are part of the stable contract.
 */
public class OlivaresApiException extends RuntimeException {

    private final int status;
    private final String code;
    private final String apiMessage;
    private final String requestId;
    private final int retryAfterSeconds;

    public OlivaresApiException(int status, String code, String message, String requestId, int retryAfterSeconds) {
        super(message + " (" + code + " " + status + ", request " + requestId + ")");
        this.status = status;
        this.code = code;
        this.apiMessage = message;
        this.requestId = requestId;
        this.retryAfterSeconds = retryAfterSeconds;
    }

    /** HTTP status code (0 when the failure is local, e.g. a non-JSON 2xx body). */
    public int getStatus() {
        return status;
    }

    /** The stable error code from the envelope, or {@code http_<status>} if none. */
    public String getCode() {
        return code;
    }

    /** The raw {@code error.message} from the envelope (without the formatted suffix). */
    public String getApiMessage() {
        return apiMessage;
    }

    /** The {@code X-Request-ID} for support correlation, or {@code ""}. */
    public String getRequestId() {
        return requestId;
    }

    /** Parsed {@code Retry-After} delta-seconds, or 0 if none/unparsable. */
    public int getRetryAfterSeconds() {
        return retryAfterSeconds;
    }
}
