// Tiny static file server for the accessibility audit (scripts/accept/phase-9.sh)
// and the screenshot run. Node only, no npm install, no dependency on python3.
//
//   node scripts/static-server.js <dir> <port>
"use strict";

const http = require("http");
const fs = require("fs");
const path = require("path");

const root = path.resolve(process.argv[2] || "web");
const port = Number(process.argv[3] || 8096);

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml"
};

http.createServer(function (req, res) {
  // Strip the query (the page reads ?theme=) and refuse anything outside root.
  const rel = decodeURIComponent(req.url.split("?")[0]).replace(/^\/+/, "") || "index.html";
  const file = path.resolve(root, rel);
  if (file !== root && !file.startsWith(root + path.sep)) {
    res.writeHead(403).end("forbidden");
    return;
  }
  fs.readFile(file, function (err, body) {
    if (err) {
      res.writeHead(404).end("not found");
      return;
    }
    res.writeHead(200, {
      "Content-Type": TYPES[path.extname(file)] || "application/octet-stream",
      "X-Content-Type-Options": "nosniff"
    });
    res.end(body);
  });
}).listen(port, "127.0.0.1", function () {
  console.log("serving " + root + " on http://127.0.0.1:" + port);
});
