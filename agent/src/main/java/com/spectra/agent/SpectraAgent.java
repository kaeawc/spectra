package com.spectra.agent;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.lang.management.ManagementFactory;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.StandardProtocolFamily;
import java.net.UnixDomainSocketAddress;
import java.net.URLDecoder;
import java.nio.ByteBuffer;
import java.nio.channels.ServerSocketChannel;
import java.nio.channels.SocketChannel;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
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
    private static final String SOCKET_PROPERTY = "spectra.agent.socket";
    private static volatile HttpServer server;
    private static volatile ServerSocketChannel unixServer;
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
        if (server != null || unixServer != null) {
            return;
        }
        Map<String, String> opts = options(args);
        token = newToken();
        if (opts.containsKey("counters")) {
            System.setProperty("spectra.agent.counters", opts.get("counters"));
        }
        if (opts.containsKey("workflows")) {
            System.setProperty("spectra.agent.workflows", opts.get("workflows"));
        }
        if ("unix".equals(opts.get("transport"))) {
            startUnix(opts);
            return;
        }
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

    private static void startUnix(Map<String, String> opts) throws Exception {
        String socketPath = opts.get("socket");
        if (empty(socketPath)) {
            socketPath = System.getProperty("java.io.tmpdir") + "/spectra-agent-" + ProcessHandle.current().pid() + ".sock";
        }
        Path path = Path.of(socketPath);
        Files.deleteIfExists(path);
        ServerSocketChannel next = ServerSocketChannel.open(StandardProtocolFamily.UNIX);
        next.bind(UnixDomainSocketAddress.of(path));
        unixServer = next;
        System.setProperty(SOCKET_PROPERTY, socketPath);
        System.setProperty(TOKEN_PROPERTY, token);
        Thread t = new Thread(SpectraAgent::serveUnix, "spectra-agent-unix");
        t.setDaemon(true);
        t.start();
    }

    private static void serveUnix() {
        while (unixServer != null && unixServer.isOpen()) {
            try (SocketChannel ch = unixServer.accept()) {
                handleUnix(ch);
            } catch (IOException ignored) {
                return;
            }
        }
    }

    private static void handleUnix(SocketChannel ch) throws IOException {
        String raw = readHTTP(ch);
        HTTPRequest req = parseHTTP(raw);
        HTTPResponse resp = route(req.method, req.path, req.query, req.headers, req.body);
        byte[] body = resp.body.getBytes(StandardCharsets.UTF_8);
        String head = "HTTP/1.1 " + resp.code + " OK\r\nContent-Type: application/json\r\nContent-Length: "
                + body.length + "\r\nConnection: close\r\n\r\n";
        ch.write(ByteBuffer.wrap(head.getBytes(StandardCharsets.UTF_8)));
        ch.write(ByteBuffer.wrap(body));
    }

    private static void health(HttpExchange exchange) throws IOException {
        write(exchange, route(exchange.getRequestMethod(), exchange.getRequestURI().getPath(), exchange.getRequestURI().getRawQuery(),
                headers(exchange), readBody(exchange)));
    }

    private static void mbeans(HttpExchange exchange) throws IOException {
        write(exchange, route(exchange.getRequestMethod(), exchange.getRequestURI().getPath(), exchange.getRequestURI().getRawQuery(),
                headers(exchange), readBody(exchange)));
    }

    private static String mbeansBody() {
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
        return out.toString();
    }

    private static void mbeanAttribute(HttpExchange exchange) throws IOException {
        write(exchange, route(exchange.getRequestMethod(), exchange.getRequestURI().getPath(), exchange.getRequestURI().getRawQuery(),
                headers(exchange), readBody(exchange)));
    }

    private static String mbeanAttributeBody(Map<String, String> q) {
        String name = q.get("name");
        String attr = q.get("attribute");
        if (empty(name) || empty(attr)) {
            return "{\"error\":\"name and attribute are required\"}";
        }
        MBeanServer mbs = ManagementFactory.getPlatformMBeanServer();
        try {
            Object value = mbs.getAttribute(new ObjectName(name), attr);
            return "{\"mbean\":\"" + json(name) + "\",\"attribute\":\"" + json(attr)
                    + "\",\"type\":\"" + json(typeName(value)) + "\",\"value\":" + valueJson(value) + "}";
        } catch (Exception e) {
            return "{\"mbean\":\"" + json(name) + "\",\"attribute\":\"" + json(attr)
                    + "\",\"error\":\"" + json(e.getClass().getSimpleName() + ": " + e.getMessage()) + "\"}";
        }
    }

    private static void mbeanOperation(HttpExchange exchange) throws IOException {
        write(exchange, route(exchange.getRequestMethod(), exchange.getRequestURI().getPath(), exchange.getRequestURI().getRawQuery(),
                headers(exchange), readBody(exchange)));
    }

    private static HTTPResponse mbeanOperationResponse(String rawBody) {
        String name = jsonField(rawBody, "name");
        String operation = jsonField(rawBody, "operation");
        if (empty(name) || empty(operation)) {
            return new HTTPResponse(400, "{\"error\":\"name and operation are required\"}");
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
                return new HTTPResponse(400, "{\"error\":\"only zero-argument MBean operations are supported\"}");
            }
            Object value = mbs.invoke(new ObjectName(name), operation, new Object[0], new String[0]);
            return new HTTPResponse(200, "{\"mbean\":\"" + json(name) + "\",\"operation\":\"" + json(operation)
                    + "\",\"type\":\"" + json(typeName(value)) + "\",\"value\":" + valueJson(value) + "}");
        } catch (Exception e) {
            return new HTTPResponse(200, "{\"mbean\":\"" + json(name) + "\",\"operation\":\"" + json(operation)
                    + "\",\"error\":\"" + json(e.getClass().getSimpleName() + ": " + e.getMessage()) + "\"}");
        }
    }

    private static void probes(HttpExchange exchange) throws IOException {
        write(exchange, route(exchange.getRequestMethod(), exchange.getRequestURI().getPath(), exchange.getRequestURI().getRawQuery(),
                headers(exchange), readBody(exchange)));
    }

    private static String probesBody() {
        Runtime rt = Runtime.getRuntime();
        return "{\"runtime\":{\"available_processors\":" + rt.availableProcessors()
                + ",\"free_memory\":" + rt.freeMemory()
                + ",\"total_memory\":" + rt.totalMemory()
                + ",\"max_memory\":" + rt.maxMemory()
                + "},\"threads\":{\"live\":" + Thread.getAllStackTraces().size() + "}"
                + ",\"counters\":" + countersJson(System.getProperty("spectra.agent.counters", ""))
                + ",\"workflows\":" + workflowsJson(System.getProperty("spectra.agent.workflows", "")) + "}";
    }

    private static HTTPResponse route(String method, String path, String rawQuery, Map<String, String> headers, String body) {
        if (!authorized(headers)) {
            return new HTTPResponse(401, "{\"error\":\"unauthorized\"}");
        }
        if ("/health".equals(path)) {
            return new HTTPResponse(200, "{\"ok\":true}");
        }
        if ("/mbeans".equals(path)) {
            return new HTTPResponse(200, mbeansBody());
        }
        if ("/mbean-attribute".equals(path)) {
            if (!"GET".equals(method)) {
                return new HTTPResponse(405, "{\"error\":\"method not allowed\"}");
            }
            return new HTTPResponse(200, mbeanAttributeBody(query(rawQuery)));
        }
        if ("/mbean-operation".equals(path)) {
            if (!"POST".equals(method)) {
                return new HTTPResponse(405, "{\"error\":\"method not allowed\"}");
            }
            return mbeanOperationResponse(body);
        }
        if ("/probes".equals(path)) {
            return new HTTPResponse(200, probesBody());
        }
        return new HTTPResponse(404, "{\"error\":\"not found\"}");
    }

    private static Map<String, String> query(String raw) {
        Map<String, String> out = new HashMap<>();
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

    private static Map<String, String> headers(HttpExchange exchange) {
        Map<String, String> out = new HashMap<>();
        for (String key : exchange.getRequestHeaders().keySet()) {
            out.put(key.toLowerCase(), exchange.getRequestHeaders().getFirst(key));
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

    private static boolean authorized(Map<String, String> headers) {
        String got = headers.get("x-spectra-agent-token");
        return token != null && token.equals(got);
    }

    private static void write(HttpExchange exchange, HTTPResponse response) throws IOException {
        byte[] bytes = response.body.getBytes(java.nio.charset.StandardCharsets.UTF_8);
        exchange.getResponseHeaders().set("Content-Type", "application/json");
        exchange.sendResponseHeaders(response.code, bytes.length);
        try (OutputStream os = exchange.getResponseBody()) {
            os.write(bytes);
        }
    }

    private static String countersJson(String spec) {
        StringBuilder out = new StringBuilder();
        out.append('[');
        boolean first = true;
        for (String part : splitSpec(spec)) {
            AgentCounterSpec counter = parseCounter(part);
            if (counter == null) {
                continue;
            }
            if (!first) {
                out.append(',');
            }
            first = false;
            out.append(counterJson(counter));
        }
        out.append(']');
        return out.toString();
    }

    private static String workflowsJson(String spec) {
        StringBuilder out = new StringBuilder();
        out.append('[');
        boolean firstWorkflow = true;
        for (String workflow : splitSpec(spec)) {
            String[] kv = workflow.split("=", 2);
            if (kv.length != 2 || empty(kv[0])) {
                continue;
            }
            if (!firstWorkflow) {
                out.append(',');
            }
            firstWorkflow = false;
            out.append("{\"name\":\"").append(json(kv[0])).append("\",\"counters\":[");
            boolean firstCounter = true;
            for (String counterSpec : kv[1].split("\\+")) {
                AgentCounterSpec counter = parseCounter(counterSpec);
                if (counter == null) {
                    continue;
                }
                if (!firstCounter) {
                    out.append(',');
                }
                firstCounter = false;
                out.append(counterJson(counter));
            }
            out.append("]}");
        }
        out.append(']');
        return out.toString();
    }

    private static String counterJson(AgentCounterSpec counter) {
        StringBuilder out = new StringBuilder();
        out.append("{\"name\":\"").append(json(counter.name)).append("\",\"mbean\":\"")
                .append(json(counter.mbean)).append("\",\"attribute\":\"").append(json(counter.attribute)).append('"');
        try {
            Object value = ManagementFactory.getPlatformMBeanServer().getAttribute(new ObjectName(counter.mbean), counter.attribute);
            out.append(",\"type\":\"").append(json(typeName(value))).append("\",\"value\":").append(valueJson(value));
        } catch (Exception e) {
            out.append(",\"error\":\"").append(json(e.getClass().getSimpleName() + ": " + e.getMessage())).append('"');
        }
        out.append('}');
        return out.toString();
    }

    private static List<String> splitSpec(String spec) {
        List<String> out = new ArrayList<>();
        if (empty(spec)) {
            return out;
        }
        for (String part : spec.split("\\|")) {
            if (!empty(part.trim())) {
                out.add(part.trim());
            }
        }
        return out;
    }

    private static AgentCounterSpec parseCounter(String spec) {
        String[] nv = spec.split("=", 2);
        if (nv.length != 2 || empty(nv[0])) {
            return null;
        }
        int attrSep = nv[1].lastIndexOf(':');
        if (attrSep <= 0 || attrSep == nv[1].length() - 1) {
            return null;
        }
        return new AgentCounterSpec(nv[0], nv[1].substring(0, attrSep), nv[1].substring(attrSep + 1));
    }

    private static Map<String, String> options(String args) {
        Map<String, String> out = new HashMap<>();
        if (empty(args)) {
            return out;
        }
        for (String part : args.split(";")) {
            String[] kv = part.split("=", 2);
            if (kv.length == 2) {
                out.put(kv[0], kv[1]);
            }
        }
        return out;
    }

    private static String readHTTP(SocketChannel ch) throws IOException {
        ByteBuffer buf = ByteBuffer.allocate(8192);
        StringBuilder out = new StringBuilder();
        while (ch.read(buf) > 0) {
            buf.flip();
            out.append(StandardCharsets.UTF_8.decode(buf));
            buf.clear();
            int headerEnd = out.indexOf("\r\n\r\n");
            if (headerEnd >= 0) {
                int len = contentLength(out.substring(0, headerEnd));
                if (out.length() >= headerEnd + 4 + len) {
                    break;
                }
            }
        }
        return out.toString();
    }

    private static HTTPRequest parseHTTP(String raw) {
        HTTPRequest req = new HTTPRequest();
        String[] parts = raw.split("\r\n\r\n", 2);
        String[] lines = parts[0].split("\r\n");
        if (lines.length > 0) {
            String[] first = lines[0].split(" ");
            if (first.length >= 2) {
                req.method = first[0];
                int q = first[1].indexOf('?');
                req.path = q >= 0 ? first[1].substring(0, q) : first[1];
                req.query = q >= 0 ? first[1].substring(q + 1) : "";
            }
        }
        for (int i = 1; i < lines.length; i++) {
            String[] hv = lines[i].split(":", 2);
            if (hv.length == 2) {
                req.headers.put(hv[0].trim().toLowerCase(), hv[1].trim());
            }
        }
        req.body = parts.length == 2 ? parts[1] : "";
        return req;
    }

    private static int contentLength(String headers) {
        for (String line : headers.split("\r\n")) {
            String[] hv = line.split(":", 2);
            if (hv.length == 2 && "content-length".equalsIgnoreCase(hv[0].trim())) {
                try {
                    return Integer.parseInt(hv[1].trim());
                } catch (NumberFormatException ignored) {
                    return 0;
                }
            }
        }
        return 0;
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

    private static final class HTTPRequest {
        String method = "";
        String path = "";
        String query = "";
        Map<String, String> headers = new HashMap<>();
        String body = "";
    }

    private static final class HTTPResponse {
        final int code;
        final String body;

        HTTPResponse(int code, String body) {
            this.code = code;
            this.body = body;
        }
    }

    private static final class AgentCounterSpec {
        final String name;
        final String mbean;
        final String attribute;

        AgentCounterSpec(String name, String mbean, String attribute) {
            this.name = name;
            this.mbean = mbean;
            this.attribute = attribute;
        }
    }
}
