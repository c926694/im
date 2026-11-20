import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const target = process.env.IM_FRONTEND_BACKEND ?? "http://127.0.0.1:8080";
const proxy = {
  "/api": { target },
  // 保留浏览器发送的 Host 和 Origin，供后端执行同源检查。
  "/ws": { target, ws: true },
};

export default defineConfig({
  plugins: [react()],
  server: { host: "127.0.0.1", proxy },
  preview: { host: "127.0.0.1", proxy },
});
