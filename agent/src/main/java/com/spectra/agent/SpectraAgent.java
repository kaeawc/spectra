package com.spectra.agent;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.lang.management.ManagementFactory;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.Executors;
import javax.management.MBeanAttributeInfo;
import javax.management.MBeanInfo;
import javax.management.MBeanOperationInfo;
import javax.management.MBeanServer;
import javax.management.ObjectName;

public final class SpectraAgent {
    private static final String PORT_PROPERTY = "spectra.agent.port";
    private static final String TOKEN_PROPERTY = "spectra.agent.token";
    private static volatile HttpServer server;
    private static volatile String token;

    private SpectraAgent() {
    }

    public static void premain(String args) throws Exception {
        start(args);
    }

    public static void agentmain(String args) throws Exception {
        start(args);
    }

    private static synchronized void start(String args) throws Exception {
        if (server != null) {
            return;
        }
        token = newToken();
        HttpServer next = HttpServer.create(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 0);
        next.createContext("/health", SpectraAgent::health);
        next.createContext("/mbeans", SpectraAgent::mbeans);
        next.createContext("/mbean-attribute", SpectraAgent::mbeanAttribute);
        next.createContext("/mbean-operation", SpectraAgent::mbeanOperation);
        next.createContext("/probes", SpectraAgent::probes);
        next.setExecutor(Executors.newSingleThreadExecutor(r -> {
            Thread t = new Thread(r, "spectra-agent-http");
            t.setDaemon(true);
            return t;
        }));
        next.start();
        server = next;
        System.setProperty(PORT_PROPERTY, Integer.toString(next.getAddress().getPort()));
        System.setProperty(TOKEN_PROPERTY, token);
    }

    private static void health(HttpExchange exchange) throws IOException {
        if (!authorized(exchange)) {
            write(exchange, 401, "{\"error\":\"unauthorized\"}");
            return;
        }
        write(exchange, 200, "{\"ok\":true}");
    }

    private static void mbeans(HttpExchange exchange) throws IOException {
        if (!authorized(exchange)) {
            write(exchange, 401, "{\"error\":\"unauthorized\"}");
            return;
        }
        MBeanServer mbs = ManagementFactory.getPlatformMBeanServer();
        Set<ObjectName> names = mbs.queryNames(null, null);
        List<ObjectName> sorted = new ArrayList<>(names);
        sorted.sort(Comparator.comparing(ObjectName::toString));

        StringBuilder out = new StringBuilder();
        out.append("{\"mbeans\":[");
        boolean firstBean = true;
        for (ObjectName name : sorted) {
            MBeanInfo info;
            try {
                info = mbs.getMBeanInfo(name);
            } catch (Exception ignored) {
                continue;
            }
            if (!firstBean) {
                out.append(',');
            }
            firstBean = false;
            out.append("{\"name\":\"").append(json(name.toString())).append('"');
            out.append(",\"class\":\"").append(json(info.getClassName())).append('"');
            out.append(",\"attributes\":[");
            boolean firstAttr = true;
            for (MBeanAttributeInfo attr : info.getAttributes()) {
                if (!firstAttr) {
                    out.append(',');
                }
                firstAttr = false;
                out.append("{\"name\":\"").append(json(attr.getName())).append('"');
                out.append(",\"type\":\"").append(json(attr.getType())).append('"');
                out.append(",\"readable\":").append(attr.isReadable());
                out.append(",\"writable\":").append(attr.isWritable()).append('}');
            }
            out.append("],\"operations\":[");
            boolean firstOp = true;
            for (MBeanOperationInfo op : info.getOperations()) {
                if (!firstOp) {
                    out.append(',');
                }
                firstOp = false;
                out.append("{\"name\":\"").append(json(op.getName())).append('"');
                out.append(",\"return_type\":\"").append(json(op.getReturnType())).append('"');
                out.append(",\"impact\":").append(op.getImpact()).append('}');
            }
            out.append("]}");
        }
        out.append("]}");
        write(exchange, 200, out.toString());
    }

    private static void mbeanAttribute(HttpExchange exchange) throws IOException {
        if (!authorized(exchange)) {
            write(exchange, 401, "{\"error\":\"unauthorized\"}");
            return;
        }
        if (!"GET".equals(exchange.getRequestMethod())) {
            write(exchange, 405, "{\"error\":\"method not allowed\"}");
            return;
        }
        Map<String, String> q = query(exchange);
        String name = q.get("name");
        String attr = q.get("attribute");
        if (empty(name) || empty(attr)) {
            write(exchange, 400, "{\"error\":\"name and attribute are required\"}");
            return;
        }
        MBeanServer mbs = ManagementFactory.getPlatformMBeanServer();
        try {
            Object value = mbs.getAttribute(new ObjectName(name), attr);
            write(exchange, 200, "{\"mbean\":\"" + json(name) + "\",\"attribute\":\"" + json(attr)
                    + "\",\"type\":\"" + json(typeName(value)) + "\",\"value\":" + valueJson(value) + "}");
        } catch (Exception e) {
            write(exchange, 200, "{\"mbean\":\"" + json(name) + "\",\"attribute\":\"" + json(attr)
                    + "\",\"error\":\"" + json(e.getClass().getSimpleName() + ": " + e.getMessage()) + "\"}");
        }
    }

    private static void mbeanOperation(HttpExchange exchange) throws IOException {
        if (!authorized(exchange)) {
            write(exchange, 401, "{\"error\":\"unauthorized\"}");
            return;
        }
        if (!"POST".equals(exchange.getRequestMethod())) {
            write(exchange, 405, "{\"error\":\"method not allowed\"}");
            return;
        }
        String rawBody = readBody(exchange);
        String name = jsonField(rawBody, "name");
        String operation = jsonField(rawBody, "operation");
        if (empty(name) || empty(operation)) {
            write(exchange, 400, "{\"error\":\"name and operation are required\"}");
            return;
        }
        MBeanServer mbs = ManagementFactory.getPlatformMBeanServer();
        try {
            MBeanInfo info = mbs.getMBeanInfo(new ObjectName(name));
            MBeanOperationInfo selected = null;
            for (MBeanOperationInfo op : info.getOperations()) {
                if (operation.equals(op.getName()) && op.getSignature().length == 0) {
                    selected = op;
                    break;
                }
            }
            if (selected == null) {
                write(exchange, 400, "{\"error\":\"only zero-argument MBean operations are supported\"}");
                return;
            }
            Object value = mbs.invoke(new ObjectName(name), operation, new Object[0], new String[0]);
            write(exchange, 200, "{\"mbean\":\"" + json(name) + "\",\"operation\":\"" + json(operation)
                    + "\",\"type\":\"" + json(typeName(value)) + "\",\"value\":" + valueJson(value) + "}");
        } catch (Exception e) {
            write(exchange, 200, "{\"mbean\":\"" + json(name) + "\",\"operation\":\"" + json(operation)
                    + "\",\"error\":\"" + json(e.getClass().getSimpleName() + ": " + e.getMessage()) + "\"}");
        }
    }

    private static void probes(HttpExchange exchange) throws IOException {
        if (!authorized(exchange)) {
            write(exchange, 401, "{\"error\":\"unauthorized\"}");
            return;
        }
        Runtime rt = Runtime.getRuntime();
        String body = "{\"runtime\":{\"available_processors\":" + rt.availableProcessors()
                + ",\"free_memory\":" + rt.freeMemory()
                + ",\"total_memory\":" + rt.totalMemory()
                + ",\"max_memory\":" + rt.maxMemory()
                + "},\"threads\":{\"live\":" + Thread.getAllStackTraces().size() + "}}";
        write(exchange, 200, body);
    }

    private static Map<String, String> query(HttpExchange exchange) {
        Map<String, String> out = new HashMap<>();
        String raw = exchange.getRequestURI().getRawQuery();
        if (raw == null || raw.isEmpty()) {
            return out;
        }
        for (String part : raw.split("&")) {
            String[] kv = part.split("=", 2);
            String key = decode(kv[0]);
            String value = kv.length == 2 ? decode(kv[1]) : "";
            out.put(key, value);
        }
        return out;
    }

    private static String readBody(HttpExchange exchange) throws IOException {
        try (InputStream in = exchange.getRequestBody()) {
            return new String(in.readAllBytes(), StandardCharsets.UTF_8);
        }
    }

    private static String jsonField(String raw, String field) {
        String needle = "\"" + field + "\"";
        int start = raw == null ? -1 : raw.indexOf(needle);
        if (start < 0) {
            return "";
        }
        int colon = raw.indexOf(':', start + needle.length());
        if (colon < 0) {
            return "";
        }
        int quote = raw.indexOf('"', colon + 1);
        if (quote < 0) {
            return "";
        }
        StringBuilder out = new StringBuilder();
        boolean escaped = false;
        for (int i = quote + 1; i < raw.length(); i++) {
            char c = raw.charAt(i);
            if (escaped) {
                out.append(c);
                escaped = false;
                continue;
            }
            if (c == '\\') {
                escaped = true;
                continue;
            }
            if (c == '"') {
                break;
            }
            out.append(c);
        }
        return out.toString();
    }

    private static String decode(String s) {
        return URLDecoder.decode(s, StandardCharsets.UTF_8);
    }

    private static boolean empty(String s) {
        return s == null || s.isEmpty();
    }

    private static String typeName(Object value) {
        return value == null ? "null" : value.getClass().getName();
    }

    private static String valueJson(Object value) {
        if (value == null) {
            return "null";
        }
        if (value instanceof Number || value instanceof Boolean) {
            return value.toString();
        }
        if (value.getClass().isArray()) {
            int n = java.lang.reflect.Array.getLength(value);
            StringBuilder out = new StringBuilder();
            out.append('[');
            for (int i = 0; i < n; i++) {
                if (i > 0) {
                    out.append(',');
                }
                out.append(valueJson(java.lang.reflect.Array.get(value, i)));
            }
            out.append(']');
            return out.toString();
        }
        return "\"" + json(String.valueOf(value)) + "\"";
    }

    private static boolean authorized(HttpExchange exchange) {
        String got = exchange.getRequestHeaders().getFirst("X-Spectra-Agent-Token");
        return token != null && token.equals(got);
    }

    private static void write(HttpExchange exchange, int code, String body) throws IOException {
        byte[] bytes = body.getBytes(java.nio.charset.StandardCharsets.UTF_8);
        exchange.getResponseHeaders().set("Content-Type", "application/json");
        exchange.sendResponseHeaders(code, bytes.length);
        try (OutputStream os = exchange.getResponseBody()) {
            os.write(bytes);
        }
    }

    private static String newToken() {
        byte[] b = new byte[32];
        new SecureRandom().nextBytes(b);
        return Base64.getUrlEncoder().withoutPadding().encodeToString(b);
    }

    private static String json(String in) {
        if (in == null) {
            return "";
        }
        StringBuilder out = new StringBuilder(in.length() + 8);
        for (int i = 0; i < in.length(); i++) {
            char c = in.charAt(i);
            switch (c) {
                case '\\':
                    out.append("\\\\");
                    break;
                case '"':
                    out.append("\\\"");
                    break;
                case '\n':
                    out.append("\\n");
                    break;
                case '\r':
                    out.append("\\r");
                    break;
                case '\t':
                    out.append("\\t");
                    break;
                default:
                    if (c < 0x20) {
                        out.append(String.format("\\u%04x", (int) c));
                    } else {
                        out.append(c);
                    }
            }
        }
        return out.toString();
    }
}
