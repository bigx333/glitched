import { defineConfig } from "vite-plus";

export default defineConfig({
  lint: {
    ignorePatterns: ["node_modules/**", "internal/webtarget/**", "testdata/**", ".agents/**"],
    options: {
      typeAware: true,
      typeCheck: true,
    },
  },
});
