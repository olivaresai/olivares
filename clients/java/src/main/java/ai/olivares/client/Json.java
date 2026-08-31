// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * A minimal, dependency-free JSON codec (RFC 8259). The SDK deliberately does
 * not bind published JSON schemas to generated beans; it maps JSON text to a generic object
 * graph ({@link Map}/{@link List}/{@link String}/{@link Long}/{@link Double}/
 * {@link Boolean}/{@code null}). Keeping this hand-written preserves the SDK's
 * zero-third-party-dependency property (the dependency surface is part of the
 * threat model) — the same stance as the Go/Python/TypeScript clients, which use
 * their language's standard-library JSON.
 *
 * <p>Parsing is strict: malformed input throws {@link JsonException}. Numbers
 * with no fraction/exponent decode to {@link Long} (falling back to
 * {@link Double} on overflow), the rest to {@link Double}.
 */
public final class Json {

    private Json() {
    }

    /** Thrown on malformed JSON (parse) or an unserializable value (write). */
    public static final class JsonException extends RuntimeException {
        JsonException(String message) {
            super(message);
        }
    }

    /**
     * Parse JSON text into a generic object graph. Objects become
     * {@link LinkedHashMap} (insertion order preserved), arrays {@link ArrayList},
     * and scalars {@link String}/{@link Long}/{@link Double}/{@link Boolean}/{@code null}.
     *
     * @throws JsonException if the text is not exactly one well-formed JSON value
     */
    public static Object parse(String text) {
        Parser p = new Parser(text);
        p.skipWhitespace();
        Object value = p.value();
        p.skipWhitespace();
        if (!p.atEnd()) {
            throw new JsonException("trailing characters after JSON value at offset " + p.pos);
        }
        return value;
    }

    /** Serialize a generic object graph to JSON text. */
    public static String write(Object value) {
        StringBuilder sb = new StringBuilder();
        writeValue(sb, value);
        return sb.toString();
    }

    // --- parsing -------------------------------------------------------------

    // Maximum object/array nesting. The parser recurses on the native JVM stack,
    // which overflows around ten thousand frames — and a StackOverflowError is an
    // Error, not a JsonException, so it would escape ClientCore's bad_response
    // mapping uncaught. Bounding nesting here (far below the stack limit, and far
    // above any realistic envelope, which is a handful of levels deep) turns a
    // hostile deeply-nested body into a clean JsonException -> bad_response, the
    // same fail-closed outcome as a truncated body. (Go's json caps at 10000; it
    // can afford that because it tracks depth on the heap, not the call stack.)
    private static final int MAX_DEPTH = 1000;

    private static final class Parser {
        private final String s;
        private int pos;
        private int depth;

        Parser(String s) {
            this.s = s;
        }

        boolean atEnd() {
            return pos >= s.length();
        }

        private char peek() {
            if (atEnd()) {
                throw new JsonException("unexpected end of JSON input");
            }
            return s.charAt(pos);
        }

        private char advance() {
            char c = peek();
            pos++;
            return c;
        }

        private void expect(char want) {
            char got = advance();
            if (got != want) {
                throw new JsonException("expected '" + want + "' but found '" + got + "' at offset " + (pos - 1));
            }
        }

        void skipWhitespace() {
            while (pos < s.length()) {
                char c = s.charAt(pos);
                if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
                    pos++;
                } else {
                    break;
                }
            }
        }

        Object value() {
            char c = peek();
            switch (c) {
                case '{':
                    return object();
                case '[':
                    return array();
                case '"':
                    return string();
                case 't':
                case 'f':
                    return bool();
                case 'n':
                    return nullLiteral();
                default:
                    if (c == '-' || (c >= '0' && c <= '9')) {
                        return number();
                    }
                    throw new JsonException("unexpected character '" + c + "' at offset " + pos);
            }
        }

        private Map<String, Object> object() {
            enter();
            try {
                expect('{');
                Map<String, Object> m = new LinkedHashMap<>();
                skipWhitespace();
                if (peek() == '}') {
                    pos++;
                    return m;
                }
                while (true) {
                    skipWhitespace();
                    if (peek() != '"') {
                        throw new JsonException("expected string object key at offset " + pos);
                    }
                    String key = string();
                    skipWhitespace();
                    expect(':');
                    skipWhitespace();
                    m.put(key, value());
                    skipWhitespace();
                    char c = advance();
                    if (c == '}') {
                        return m;
                    }
                    if (c != ',') {
                        throw new JsonException("expected ',' or '}' in object at offset " + (pos - 1));
                    }
                }
            } finally {
                depth--;
            }
        }

        private List<Object> array() {
            enter();
            try {
                expect('[');
                List<Object> a = new ArrayList<>();
                skipWhitespace();
                if (peek() == ']') {
                    pos++;
                    return a;
                }
                while (true) {
                    skipWhitespace();
                    a.add(value());
                    skipWhitespace();
                    char c = advance();
                    if (c == ']') {
                        return a;
                    }
                    if (c != ',') {
                        throw new JsonException("expected ',' or ']' in array at offset " + (pos - 1));
                    }
                }
            } finally {
                depth--;
            }
        }

        // enter bounds nesting: increment on entering a container, throw past the
        // cap (a JsonException, so ClientCore maps it to bad_response rather than
        // letting native-stack recursion overflow with an uncatchable Error).
        private void enter() {
            if (++depth > MAX_DEPTH) {
                throw new JsonException("maximum nesting depth (" + MAX_DEPTH + ") exceeded at offset " + pos);
            }
        }

        private String string() {
            expect('"');
            StringBuilder sb = new StringBuilder();
            while (true) {
                char c = advance();
                if (c == '"') {
                    return sb.toString();
                }
                if (c == '\\') {
                    char esc = advance();
                    switch (esc) {
                        case '"':
                            sb.append('"');
                            break;
                        case '\\':
                            sb.append('\\');
                            break;
                        case '/':
                            sb.append('/');
                            break;
                        case 'b':
                            sb.append('\b');
                            break;
                        case 'f':
                            sb.append('\f');
                            break;
                        case 'n':
                            sb.append('\n');
                            break;
                        case 'r':
                            sb.append('\r');
                            break;
                        case 't':
                            sb.append('\t');
                            break;
                        case 'u':
                            sb.append(hex4());
                            break;
                        default:
                            throw new JsonException("invalid escape '\\" + esc + "' at offset " + (pos - 1));
                    }
                } else if (c < 0x20) {
                    throw new JsonException("unescaped control character (U+" + Integer.toHexString(c)
                            + ") in string at offset " + (pos - 1));
                } else {
                    // A UTF-16 code unit (incl. lone halves of a surrogate pair) is
                    // appended verbatim — a 😀 pair reconstructs correctly.
                    sb.append(c);
                }
            }
        }

        private char hex4() {
            if (pos + 4 > s.length()) {
                throw new JsonException("truncated \\u escape at offset " + pos);
            }
            int v = 0;
            for (int i = 0; i < 4; i++) {
                char c = s.charAt(pos++);
                int d;
                if (c >= '0' && c <= '9') {
                    d = c - '0';
                } else if (c >= 'a' && c <= 'f') {
                    d = c - 'a' + 10;
                } else if (c >= 'A' && c <= 'F') {
                    d = c - 'A' + 10;
                } else {
                    throw new JsonException("invalid hex digit '" + c + "' in \\u escape at offset " + (pos - 1));
                }
                v = v * 16 + d;
            }
            return (char) v;
        }

        private Object number() {
            int start = pos;
            if (peek() == '-') {
                pos++;
            }
            if (atEnd()) {
                throw new JsonException("truncated number at offset " + start);
            }
            char c = s.charAt(pos);
            if (c == '0') {
                pos++;
            } else if (c >= '1' && c <= '9') {
                while (!atEnd() && isDigit(s.charAt(pos))) {
                    pos++;
                }
            } else {
                throw new JsonException("invalid number at offset " + start);
            }
            boolean isDouble = false;
            if (!atEnd() && s.charAt(pos) == '.') {
                isDouble = true;
                pos++;
                if (atEnd() || !isDigit(s.charAt(pos))) {
                    throw new JsonException("invalid fraction at offset " + pos);
                }
                while (!atEnd() && isDigit(s.charAt(pos))) {
                    pos++;
                }
            }
            if (!atEnd() && (s.charAt(pos) == 'e' || s.charAt(pos) == 'E')) {
                isDouble = true;
                pos++;
                if (!atEnd() && (s.charAt(pos) == '+' || s.charAt(pos) == '-')) {
                    pos++;
                }
                if (atEnd() || !isDigit(s.charAt(pos))) {
                    throw new JsonException("invalid exponent at offset " + pos);
                }
                while (!atEnd() && isDigit(s.charAt(pos))) {
                    pos++;
                }
            }
            String num = s.substring(start, pos);
            if (!isDouble) {
                try {
                    return Long.parseLong(num);
                } catch (NumberFormatException overflow) {
                    return Double.parseDouble(num);
                }
            }
            return Double.parseDouble(num);
        }

        private Object bool() {
            if (s.startsWith("true", pos)) {
                pos += 4;
                return Boolean.TRUE;
            }
            if (s.startsWith("false", pos)) {
                pos += 5;
                return Boolean.FALSE;
            }
            throw new JsonException("invalid literal at offset " + pos);
        }

        private Object nullLiteral() {
            if (s.startsWith("null", pos)) {
                pos += 4;
                return null;
            }
            throw new JsonException("invalid literal at offset " + pos);
        }

        private static boolean isDigit(char c) {
            return c >= '0' && c <= '9';
        }
    }

    // --- serialization -------------------------------------------------------

    private static final char[] HEX = "0123456789abcdef".toCharArray();

    private static void writeValue(StringBuilder sb, Object v) {
        if (v == null) {
            sb.append("null");
        } else if (v instanceof CharSequence) {
            writeString(sb, v.toString());
        } else if (v instanceof Boolean) {
            sb.append(((Boolean) v) ? "true" : "false");
        } else if (v instanceof Double || v instanceof Float) {
            double d = ((Number) v).doubleValue();
            if (Double.isNaN(d) || Double.isInfinite(d)) {
                throw new JsonException("non-finite number is not valid JSON: " + v);
            }
            sb.append(v instanceof Float ? Float.toString((Float) v) : Double.toString((Double) v));
        } else if (v instanceof Number) {
            // Long/Integer/Short/Byte/BigInteger/BigDecimal render their own text.
            sb.append(v.toString());
        } else if (v instanceof Map) {
            writeObject(sb, (Map<?, ?>) v);
        } else if (v instanceof Iterable) {
            writeArray(sb, (Iterable<?>) v);
        } else if (v instanceof Object[]) {
            writeArray(sb, java.util.Arrays.asList((Object[]) v));
        } else {
            throw new JsonException("cannot serialize " + v.getClass().getName() + " to JSON");
        }
    }

    private static void writeObject(StringBuilder sb, Map<?, ?> m) {
        sb.append('{');
        boolean first = true;
        for (Map.Entry<?, ?> e : m.entrySet()) {
            if (!first) {
                sb.append(',');
            }
            first = false;
            Object key = e.getKey();
            if (key == null) {
                throw new JsonException("JSON object key must not be null");
            }
            writeString(sb, key.toString());
            sb.append(':');
            writeValue(sb, e.getValue());
        }
        sb.append('}');
    }

    private static void writeArray(StringBuilder sb, Iterable<?> it) {
        sb.append('[');
        boolean first = true;
        for (Object e : it) {
            if (!first) {
                sb.append(',');
            }
            first = false;
            writeValue(sb, e);
        }
        sb.append(']');
    }

    private static void writeString(StringBuilder sb, String s) {
        sb.append('"');
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"':
                    sb.append("\\\"");
                    break;
                case '\\':
                    sb.append("\\\\");
                    break;
                case '\b':
                    sb.append("\\b");
                    break;
                case '\f':
                    sb.append("\\f");
                    break;
                case '\n':
                    sb.append("\\n");
                    break;
                case '\r':
                    sb.append("\\r");
                    break;
                case '\t':
                    sb.append("\\t");
                    break;
                default:
                    if (c < 0x20) {
                        sb.append("\\u00").append(HEX[(c >> 4) & 0xF]).append(HEX[c & 0xF]);
                    } else {
                        sb.append(c);
                    }
            }
        }
        sb.append('"');
    }
}
