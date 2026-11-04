// Vite library build for one RutOS WebUI plugin.
//
// The host WebUI already ships Vue, so vue is externalised (vite-plugin-externals)
// and the bundle is emitted as a single ES module. compile-vuci-app.sh sets
// VITE_PLUGIN_ENTRY / VITE_PLUGIN_FILENAME per component.
import { defineConfig, loadEnv } from "vite";
import vue from "@vitejs/plugin-vue";
import { viteExternalsPlugin } from "vite-plugin-externals";
import { compression } from "vite-plugin-compression2";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  return {
    plugins: [
      vue({ isProduction: true }),
      viteExternalsPlugin({ vue: "Vue" }),
      compression({ deleteOriginalAssets: true }),
    ],
    define: { "process.env.NODE_ENV": '"production"' },
    build: {
      cssCodeSplit: false,
      lib: {
        entry: env.VITE_PLUGIN_ENTRY,
        fileName: () => `${env.VITE_PLUGIN_FILENAME}.js`,
        formats: ["es"],
      },
      rollupOptions: {
        external: ["vue"],
        output: { globals: { vue: "Vue" } },
      },
    },
  };
});
