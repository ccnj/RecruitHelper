//go:build !windows

package handinput

// 非 Windows 没有 TIP:自研输入法是 Windows TSF 的 DLL,命名管道也是 Windows 的。
//
// **这里不是"占位存根",是一个如实的答案。** macOS 开发机上打字走系统输入法,
// 上屏哪个词由它决定、我方不可控——这正是段一真机里「加个」出成「价格」的原因,
// 是预期行为,不是缺陷。所以 `wordDriver` 在这边根本不实现:Service 会看到
// "本平台不驱动上屏词",照常打字,回读对不上如实报 matched=false。
//
// 反过来在 Windows 上,没有 TIP 就**不打**——理由见 tip_windows.go 的 DriveWords。
