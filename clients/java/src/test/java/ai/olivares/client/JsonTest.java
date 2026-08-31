// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;

/** Unit tests for the dependency-free JSON codec. */
class JsonTest {

    @Test
    void parsesObjectWithScalars() {
        Object v = Json.parse("{\"a\":1,\"b\":\"x\",\"c\":true,\"d\":null,\"e\":1.5,\"f\":false}");
        Map<?, ?> m = assertInstanceOf(Map.class, v);
        assertEquals(1L, m.get("a"));
        assertEquals("x", m.get("b"));
        assertEquals(Boolean.TRUE, m.get("c"));
        assertNull(m.get("d"));
        assertEquals(1.5, m.get("e"));
        assertEquals(Boolean.FALSE, m.get("f"));
    }

    @Test
    void preservesObjectKeyOrder() {
        Object v = Json.parse("{\"z\":1,\"a\":2,\"m\":3}");
        assertEquals("[z, a, m]", ((Map<?, ?>) v).keySet().toString());
    }

    @Test
    void parsesNestedContainers() {
        Object v = Json.parse("{\"items\":[{\"id\":\"a\"},{\"id\":\"b\"}],\"n\":[1,2,3],\"deep\":{\"x\":[]}}");
        Map<?, ?> m = (Map<?, ?>) v;
        assertEquals(2, ((List<?>) m.get("items")).size());
        assertEquals(List.of(1L, 2L, 3L), m.get("n"));
        assertTrue(((List<?>) ((Map<?, ?>) m.get("deep")).get("x")).isEmpty());
    }

    @Test
    void parsesEscapesAndUnicode() {
        assertEquals("a\"b\\c/\b\f\n\r\t", Json.parse("\"a\\\"b\\\\c\\/\\b\\f\\n\\r\\t\""));
        assertEquals("A", Json.parse("\"\\u0041\""));
        // A surrogate pair reconstructs the full code point (😀, U+1F600).
        assertEquals("😀", Json.parse("\"\\uD83D\\uDE00\""));
        // Raw (already-UTF-16) non-ASCII passes through verbatim.
        assertEquals("ñ€", Json.parse("\"ñ€\""));
    }

    @Test
    void parsesNumbers() {
        assertEquals(42L, Json.parse("42"));
        assertEquals(-7L, Json.parse("-7"));
        assertEquals(0L, Json.parse("0"));
        assertEquals(3.14, Json.parse("3.14"));
        assertEquals(1000.0, Json.parse("1e3"));
        assertEquals(-2.5e-3, Json.parse("-2.5e-3"));
        // Beyond Long range, fall back to Double rather than fail.
        assertInstanceOf(Double.class, Json.parse("99999999999999999999"));
        assertInstanceOf(Long.class, Json.parse("9223372036854775807"));
    }

    @Test
    void parsesWithSurroundingWhitespace() {
        assertEquals(1L, Json.parse("  \n\t 1 \r\n "));
    }

    @Test
    void rejectsMalformed() {
        for (String bad : new String[]{
                "", "   ", "{", "[1,]", "{\"a\":}", "tru", "nul", "01", "{\"a\":1}x",
                "\"unterminated", "{a:1}", "1.", "1e", "[1 2]", "{\"a\"1}", "+1", "."
        }) {
            assertThrows(Json.JsonException.class, () -> Json.parse(bad), "should reject: " + bad);
        }
    }

    @Test
    void writesScalars() {
        assertEquals("null", Json.write(null));
        assertEquals("true", Json.write(true));
        assertEquals("false", Json.write(false));
        assertEquals("42", Json.write(42L));
        assertEquals("42", Json.write(42));
        assertEquals("\"a\\\"b\\\\c\"", Json.write("a\"b\\c"));
        // Control characters become a six-char escape; printable non-ASCII is emitted raw.
        assertEquals("\"\\u0001\"", Json.write("\u0001"));
        assertEquals("\"ñ\"", Json.write("ñ"));
    }

    @Test
    void writesContainersInInsertionOrder() {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("a", 1L);
        m.put("b", List.of("x", 2L));
        m.put("c", null);
        assertEquals("{\"a\":1,\"b\":[\"x\",2],\"c\":null}", Json.write(m));
    }

    @Test
    void writeRejectsNonFiniteAndUnknownTypes() {
        assertThrows(Json.JsonException.class, () -> Json.write(Double.NaN));
        assertThrows(Json.JsonException.class, () -> Json.write(Double.POSITIVE_INFINITY));
        assertThrows(Json.JsonException.class, () -> Json.write(new Object()));
    }

    @Test
    void roundTripsCompactDocument() {
        String src = "{\"items\":[{\"id\":\"a\"},{\"id\":\"b\"}],\"cursor\":\"c1\",\"has_more\":true,\"n\":3}";
        assertEquals(src, Json.write(Json.parse(src)));
    }

    @Test
    void emptyContainersParseAndWrite() {
        assertEquals("{}", Json.write(Json.parse("{}")));
        assertEquals("[]", Json.write(Json.parse("[]")));
        assertFalse(((Map<?, ?>) Json.parse("{}")).containsKey("x"));
    }

    @Test
    void rejectsPathologicallyDeepNestingAsExceptionNotStackOverflow() {
        // A hostile/huge body must become a JsonException (which the core maps to
        // bad_response), never an uncatchable StackOverflowError from native recursion.
        String deep = "[".repeat(100_000);
        assertThrows(Json.JsonException.class, () -> Json.parse(deep));
    }

    @Test
    void parsesRealisticNesting() {
        // Comfortably under the depth cap (real envelopes are a few levels deep).
        Object v = Json.parse("[".repeat(500) + "]".repeat(500));
        assertInstanceOf(List.class, v);
    }
}
