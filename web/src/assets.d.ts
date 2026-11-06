// esbuild's text loader turns an imported .svg into its source string.
declare module "*.svg" {
  const source: string;
  export default source;
}
