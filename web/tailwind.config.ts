import type { Config } from "tailwindcss";
import { heroui } from "@heroui/theme";

// Brand palette: accent #009B97 on a near-black #101010 canvas.
const config: Config = {
  content: [
    "./app/**/*.{js,ts,jsx,tsx,mdx}",
    "./components/**/*.{js,ts,jsx,tsx,mdx}",
    "./node_modules/@heroui/theme/dist/components/(button|card|progress|snippet|chip).js",
  ],
  theme: {
    extend: {
      colors: {
        canvas: "#101010",
        accent: {
          50: "#e6f5f5",
          100: "#c2e8e7",
          200: "#8fd6d4",
          300: "#5cc4c1",
          400: "#29b2ae",
          500: "#009B97",
          600: "#00807d",
          700: "#006563",
          800: "#004a48",
          900: "#002f2e",
          DEFAULT: "#009B97",
          foreground: "#03110f",
        },
      },
      fontFamily: {
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "Consolas", "monospace"],
      },
    },
  },
  darkMode: "class",
  plugins: [
    heroui({
      defaultTheme: "dark",
      themes: {
        dark: {
          colors: {
            background: "#101010",
            foreground: "#ededed",
            focus: "#009B97",
            content1: "#171717",
            content2: "#1d1d1d",
            content3: "#242424",
            content4: "#2b2b2b",
            primary: {
              50: "#e6f5f5",
              100: "#c2e8e7",
              200: "#8fd6d4",
              300: "#5cc4c1",
              400: "#29b2ae",
              500: "#009B97",
              600: "#00807d",
              700: "#006563",
              800: "#004a48",
              900: "#002f2e",
              DEFAULT: "#009B97",
              foreground: "#03110f",
            },
          },
        },
      },
    }),
  ],
};

export default config;
