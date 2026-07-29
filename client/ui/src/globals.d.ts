// 由 vite.config.ts 的 define 在构建期替换为 client/ui/package.json 的 version。
// 版本号在 UI 里只应经由这个常量取用，不要再写字面量——升版本时漏改一处，
// 界面就会稳定地报出一个错误的版本号。
declare const __APP_VERSION__: string
