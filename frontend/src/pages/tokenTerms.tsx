// Paket Token terms: the "Paket Token" part of the terms page at
// /ketentuan#paket-token, which the portal checkout links to. Bump
// TOKEN_TERMS_VERSION together with tokenpkg.TermsVersion in the backend
// whenever this text changes materially.

import { TELEGRAM_URL } from "../components/PublicLayout";
import type { TermsSection } from "./hostingTerms";

export const TOKEN_EFFECTIVE_DATE = "11 November 2026";
export const TOKEN_TERMS_VERSION = "2026-11-11";

const LINK = "text-[var(--link)] underline underline-offset-2 hover:opacity-80";

export const TOKEN_SECTIONS: TermsSection[] = [
  {
    id: "paket-token-penerimaan",
    title: "Penerimaan ketentuan",
    body: (
      <>
        <p>
          Bagian ini berlaku khusus untuk Paket Token dan melengkapi ketentuan umum di atas. Jika ada yang berbeda,
          bagian ini yang berlaku untuk Paket Token.
        </p>
        <p>
          Anda menyetujui bagian ini dengan mencentang kotak persetujuan setiap kali membeli paket. Setiap pesanan
          mencatat versi ketentuan yang Anda setujui beserta waktunya.
        </p>
      </>
    ),
  },
  {
    id: "paket-token-layanan",
    title: "Layanan",
    body: (
      <>
        <p>
          Paket Token adalah token prabayar untuk model yang tercantum di halaman{" "}
          <a href="/paket-token" className={LINK}>Paket Token</a> dan di portal. Pembelian pertama yang dibayar membuat
          satu key token untuk akun Anda. Key ini terpisah dari API key saldo (pay-as-you-go).
        </p>
        <ul className="list-disc space-y-2 pl-5">
          <li>Key token hanya bisa dipakai untuk model yang tercantum. Permintaan ke model lain ditolak.</li>
          <li>
            Key token hanya bisa dipakai untuk endpoint percakapan (chat). Endpoint gambar, audio, embedding, dan
            pencarian tidak tersedia untuk key token.
          </li>
          <li>
            Pemakaian key token tidak memotong saldo, dan pemakaian API key saldo tidak memotong token. Token tidak
            dapat ditukar menjadi saldo, dan saldo tidak dapat ditukar menjadi token.
          </li>
        </ul>
      </>
    ),
  },
  {
    id: "paket-token-sesuai-pembelian",
    title: "Paket sesuai pembelian",
    body: (
      <p>
        Jumlah token, masa aktif, dan harga yang Anda dapat adalah yang tertera saat checkout. Jika kami mengubah atau
        menghapus paket setelah Anda checkout, pesanan Anda tetap diproses sesuai isi paket saat checkout.
      </p>
    ),
  },
  {
    id: "paket-token-masa-aktif",
    title: "Masa aktif",
    body: (
      <>
        <p>
          Masa aktif tertera di setiap paket, bawaannya 15 hari. Masa aktif mulai dihitung saat pembayaran paket pertama
          dalam satu periode kami terima.
        </p>
        <p>
          Saat masa aktif berakhir, sisa token hangus dan key token berhenti bekerja. Permintaan dengan key tersebut
          ditolak sampai Anda membeli paket token baru.
        </p>
      </>
    ),
  },
  {
    id: "paket-token-tambah",
    title: "Tambah token dan perpanjangan",
    body: (
      <ul className="list-disc space-y-2 pl-5">
        <li>
          Membeli paket token lagi saat paket masih aktif hanya menambah token. Tanggal berakhir tidak berubah.
        </li>
        <li>
          Paket masa aktif (misalnya +15 hari) hanya memperpanjang tanggal berakhir dan tidak menambah token. Paket ini
          hanya bisa dibeli saat paket token masih aktif.
        </li>
        <li>
          Membeli paket token setelah masa aktif berakhir memulai periode baru dengan masa aktif dari paket itu. Sisa
          token dari periode lama tidak ikut terbawa.
        </li>
      </ul>
    ),
  },
  {
    id: "paket-token-perhitungan",
    title: "Perhitungan token",
    body: (
      <>
        <p>
          Token yang dihitung adalah token input ditambah token output menurut pencatatan gateway kami, termasuk token
          reasoning dan token input yang di-cache. Hitungannya 1 token = 1 token untuk semua model yang tercantum, tanpa
          bobot per model.
        </p>
        <p>Sisa token dan tanggal berakhir dapat Anda lihat di portal.</p>
      </>
    ),
  },
  {
    id: "paket-token-permintaan",
    title: "Batas per permintaan",
    body: (
      <ul className="list-disc space-y-2 pl-5">
        <li>
          Sebelum diteruskan, setiap permintaan butuh sisa token yang cukup untuk perkiraan token input ditambah batas
          output-nya. Jika tidak cukup, permintaan ditolak dan tidak ada token yang dipotong.
        </li>
        <li>
          Batas output (misalnya <code className="font-mono text-[13px]">max_tokens</code>) kami turunkan agar
          perkiraan input ditambah output tidak melebihi sisa token. Jika tidak Anda isi, kami memakai batas output
          bawaan model.
        </li>
        <li>Setiap permintaan hanya boleh meminta satu jawaban.</li>
        <li>
          Permintaan yang terputus setelah terkirim ke penyedia, karena Anda menghentikannya, kena timeout, atau
          koneksinya putus, dihitung sebesar perkiraan token input ditambah seluruh batas output-nya. Jika output yang
          sudah terkirim lebih besar, yang dihitung adalah output tersebut. Hal yang sama berlaku untuk stream yang
          selesai tanpa laporan pemakaian dari penyedia.
        </li>
        <li>Permintaan yang gagal di sisi penyedia sebelum ada output sama sekali tidak dihitung.</li>
      </ul>
    ),
  },
  {
    id: "paket-token-key",
    title: "Key token",
    body: (
      <>
        <p>
          Key token tunduk pada batas jumlah permintaan per menit dan jumlah permintaan bersamaan yang kami tetapkan.
          Batas ini dapat kami ubah sewaktu-waktu.
        </p>
        <p>
          Key token bersifat rahasia seperti API key saldo. Semua permintaan dengan key token Anda dianggap berasal dari
          Anda dan memotong token Anda.
        </p>
        <p>
          Kami dapat menonaktifkan key token yang melanggar ketentuan. Selama nonaktif, permintaan dengan key tersebut
          ditolak, Anda tidak dapat membeli paket untuknya, dan masa aktif tetap berjalan.
        </p>
      </>
    ),
  },
  {
    id: "paket-token-pembayaran",
    title: "Pembayaran",
    body: (
      <>
        <p>
          Paket dibayar di muka melalui gerbang pembayaran di portal. Tautan pembayaran berlaku untuk waktu terbatas.
          Tautan yang lewat batas waktu batal dengan sendirinya tanpa biaya.
        </p>
        <p>
          Pembayaran yang tidak dapat diterapkan tidak menambah token maupun masa aktif. Contohnya pembayaran paket masa
          aktif yang baru kami terima setelah paket Anda berakhir. Pembayaran seperti ini kami tahan untuk ditinjau
          admin. Hubungi kami melalui{" "}
          <a href={TELEGRAM_URL} target="_blank" rel="noopener noreferrer" className={LINK}>Telegram</a> dengan bukti
          pembayaran.
        </p>
        <p>Token tidak dapat diuangkan atau dipindahkan ke akun lain.</p>
      </>
    ),
  },
  {
    id: "paket-token-harga",
    title: "Perubahan harga dan paket",
    body: (
      <p>
        Kami dapat mengubah harga, isi, dan daftar paket, serta daftar model yang tercantum. Perubahan harga dan isi
        paket berlaku untuk checkout berikutnya, tidak untuk pesanan yang sudah Anda checkout.
      </p>
    ),
  },
  {
    id: "paket-token-perubahan",
    title: "Perubahan ketentuan",
    body: (
      <p>
        Kami dapat memperbarui bagian ini. Versi baru berlaku untuk pembelian berikutnya, dan Anda akan diminta
        menyetujuinya lagi saat itu.
      </p>
    ),
  },
];
