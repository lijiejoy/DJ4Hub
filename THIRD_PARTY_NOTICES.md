# Third-Party Notices

DJ 4G Hub contains code derived from the upstream VoHive project and work inspired by ZenGeekLabs/DJOneHub. It retains the license and required notice provided in the repository root [`LICENSE`](LICENSE):

```text
Required Notice: Copyright iniwex5 (https://github.com/iniwex5/vohive)
```

## Release Runtime

The legacy arm64 CLI package includes **libusb 1.0.30**, distributed under the GNU Lesser General Public License, version 2.1 or later. The complete App bundles the libusb installed on its build host; its actual version is recorded in `Contents/Resources/BuildInfo.plist`.

- Project: <https://libusb.info/>
- Source: <https://github.com/libusb/libusb/releases/tag/v1.0.30>
- License text in the CLI package: `licenses/libusb-COPYING`; in the App: `Contents/Resources/licenses/libusb-COPYING`

The complete App also bundles ADB from Android platform-tools. Its corresponding NOTICE is retained at `Contents/Resources/licenses/platform-tools-NOTICE.txt`. Module-side voice runtime downloads are separate from the App bundle.

## Vendored Source Dependencies

The source repository includes vendored dependencies under `third_party/` so the versions used by DJ 4G Hub remain reproducible. Their original copyright notices and license texts are retained in the corresponding directories.

| Component | License file |
| --- | --- |
| euicc-go | `third_party/euicc-go/LICENSE` |
| uicc-go | `third_party/uicc-go/LICENSE` |
| quectel-qmi-go | `third_party/quectel-qmi-go/LICENSE` |
| strftime | `third_party/strftime/LICENSE` |
| pkg/errors | `third_party/pkg-errors/LICENSE` |
| golang.org/x/sys | `third_party/x-sys/LICENSE` |
| golang.org/x/text | `third_party/x-text/LICENSE` |
| multierr | `third_party/multierr/LICENSE.txt` |

Dependencies fetched through Go modules retain their own licenses and copyright notices. This file is informational and does not replace any component's full license text.

The complete App build copies root LICENSE/COPYING/NOTICE files from the Go modules selected by the macOS service dependency graph into `Contents/Resources/licenses/go/`, in addition to this notice and the project's license.
