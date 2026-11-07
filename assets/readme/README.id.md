<h1 align="center"><a href="https://releem.com/"><img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="24" height="24" alt="Logo Releem" /></a> Releem</h1>

**Alat optimasi kinerja MySQL dengan AI dan otomatisasi terkendali**

Releem adalah alat optimasi kinerja MySQL dengan AI dan otomatisasi terkendali, yang menjadi asisten basis data Anda sejak keputusan optimasi pertama dan seterusnya.

Untuk engineer, DBA, dan tim infrastruktur yang bertanggung jawab atas kinerja MySQL di lingkungan produksi. Tingkatkan kinerja kueri dan efisiensi sumber daya dengan rekomendasi berdasarkan beban kerja, lalu tinjau kembali hal yang perlu ditangani saat kueri, trafik, dan data berubah.

<p align="center">
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MySQL-4479A1?logo=mysql&amp;logoColor=white" alt="MySQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MariaDB-003545?logo=mariadb&amp;logoColor=white" alt="MariaDB" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/PostgreSQL-336791?logo=postgresql&amp;logoColor=white" alt="PostgreSQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/Percona_Server_for_MySQL-orange" alt="Percona Server for MySQL" /></a>
  <br />
  <a href="https://hub.docker.com/r/releem/releem-agent"><img src="https://img.shields.io/docker/pulls/releem/releem-agent?logo=docker&amp;logoColor=white" alt="Jumlah pull Docker" /></a>
    <a href="https://docs.releem.com/installation"><img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows%20%7C%20Docker%20%7C%20K8s%20%7C%20AWS%20%7C%20GCP%20%7C%20Azure%20%7C%20WHM%2FcPanel%20%7C%20Clusters-555555" alt="Platform: Linux, Windows, Docker, Kubernetes, Amazon RDS, Amazon Aurora, Google Cloud SQL, Azure Database for MySQL, WHM/cPanel, dan klaster" /></a>
  <a href="https://github.com/Releem/releem-agent/stargazers"><img src="https://img.shields.io/github/stars/Releem/releem-agent?style=flat&amp;logo=github" alt="Bintang GitHub" /></a>
  <a href="https://github.com/Releem/releem-agent"><img src="https://img.shields.io/badge/Agent-open_source-2ea44f?logo=github" alt="Agent sumber terbuka" /></a>
  <a href="https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ"><img src="https://img.shields.io/badge/Slack-Join_community-4A154B?logo=slack&amp;logoColor=white" alt="Komunitas Slack" /></a>
</p>

<p align="center">
  <a href="../../README.md">English</a> •
  <a href="README.ru.md">Русский</a> •
  <a href="README.es.md">Español</a> •
  <a href="README.pt.md">Português</a> •
  <a href="README.zh.md">简体中文</a> •
  <a href="README.fr.md">Français</a> •
  <b>Bahasa Indonesia</b>
</p>

<p align="center">
  <a href="#getting-started">Mulai menggunakan</a> |
  <a href="https://docs.releem.com/">Dokumentasi</a> |
  <a href="https://releem.com/mysql-configuration-tuning">Penyetelan konfigurasi MySQL</a> |
  <a href="https://releem.com/sql-query-optimization">Optimasi kueri SQL</a> |
  <a href="https://releem.com/compare/mysqltuner">Bandingkan dengan MySQLTuner</a> |
  <a href="#security">Keamanan</a>
</p>

Repositori ini berisi **Releem Agent** sumber terbuka, yang menghubungkan basis data Anda ke layanan berlangganan Releem yang dihosting. Repositori ini juga menjadi tempat masukan, Issues, dan diskusi roadmap untuk seluruh produk Releem.

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%" alt="Dasbor Releem" />
</p>

## ⚡ Mengapa Releem?

- **[Optimasi kueri dan indeks](https://releem.com/sql-query-optimization):** Tinjau rekomendasi kueri dan [indeks](https://docs.releem.com/recommendations) berdasarkan beban kerja untuk meningkatkan kinerja kueri MySQL.
- **[Penyetelan konfigurasi MySQL](https://releem.com/mysql-configuration-tuning):** Tinjau pengaturan berdasarkan beban kerja, dengan penyetelan yang ditingkatkan melalui pembelajaran mesin, untuk meningkatkan efisiensi sumber daya.
- **Otomatisasi terkendali:** Terapkan perubahan konfigurasi yang didukung serta perubahan skema yang telah disetujui dan memenuhi syarat melalui alur kerja yang terdokumentasi, sehingga tim Anda tetap mengendalikan apa yang diubah dan kapan perubahan dilakukan.
- **[Pemantauan dan pemeriksaan kesehatan](https://releem.com/health-checks):** Gunakan metrik basis data dan pemeriksaan kesehatan untuk mengidentifikasi hal yang perlu ditangani, lalu bandingkan kinerja dan penggunaan sumber daya yang teramati setelah perubahan.

Lihat [proses penyetelan konfigurasi](https://docs.releem.com/recommendations/configuration-tuning/mysql-tuning-process) dan [alur kerja eksekusi perubahan skema](https://docs.releem.com/recommendations/query-optimization/automatic-schema-changes) untuk mengetahui perubahan yang didukung dan langkah penerapannya.

<p align="center">
  <strong>Temukan dan terapkan indeks MySQL yang belum tersedia dengan Releem</strong><br /><br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">
    <img src="https://img.youtube.com/vi/Pd-DUK0so9Y/hqdefault.jpg" width="480" alt="Lihat cara menemukan dan menerapkan indeks MySQL yang belum tersedia dengan Releem" />
  </a>
  <br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">▶ Tonton: temukan dan terapkan indeks MySQL yang belum tersedia dengan Releem</a>
</p>

<p align="center">
  <strong>Terapkan konfigurasi MySQL yang direkomendasikan</strong><br /><br />
  <a href="https://youtu.be/XqBZPGRlrGs">
    <img src="https://raw.githubusercontent.com/Releem/docs/main/assets/images/applying-conf-dark.gif"
         width="80%"
         alt="Dasbor Releem yang menunjukkan cara meninjau alasan rekomendasi, memeriksa apakah perlu memulai ulang, dan menerapkan konfigurasi MySQL yang direkomendasikan" />
  </a>
  <br />
  <a href="https://youtu.be/XqBZPGRlrGs">▶ Tonton demo 15 detik di YouTube</a>
</p>

<p align="center">
  <strong>Terapkan konfigurasi melalui CLI Releem Agent</strong><br /><br />
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%" alt="Menerapkan konfigurasi dengan Releem Agent" />
</p>

## 🧭 Cara kerja

1. **Menganalisis beban kerja MySQL Anda.** Mengumpulkan aktivitas kueri, pengaturan basis data, dan metrik sumber daya untuk memahami cara basis data Anda berjalan.
2. **Mengidentifikasi peluang optimasi kinerja.** Menampilkan kueri yang lambat atau sering dijalankan serta temuan konfigurasi yang dapat memengaruhi kinerja.
3. **Membantu menentukan prioritas penanganan.** Menggunakan pengamatan beban kerja dan temuan untuk membantu Anda memilih hal yang perlu ditangani terlebih dahulu.
4. **Merekomendasikan perubahan kueri, indeks, dan konfigurasi.** Menyajikan usulan perbaikan beserta penjelasan untuk ditinjau oleh tim Anda.
5. **Membantu menerapkan perubahan yang didukung.** Terapkan perubahan konfigurasi yang didukung dan perubahan skema yang telah disetujui melalui otomatisasi terkendali.
6. **Membantu Anda mengukur dampaknya.** Bandingkan kinerja kueri, penggunaan sumber daya, dan pengaturan yang benar-benar berlaku setelah perubahan.

<a name="getting-started"></a>

## 🚀 Mulai menggunakan Releem

1. **Buat akun Anda.** [Daftar ke Releem](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#).
2. **Hubungkan basis data Anda.** Instal Releem Agent menggunakan [panduan instalasi](https://docs.releem.com/installation) yang sesuai dengan lingkungan Anda.
3. **Periksa koneksi.** Pastikan status Agent adalah Connected dan metrik terkini sudah masuk ke dasbor.
4. **Tinjau dan terapkan rekomendasi.** Tinjau rekomendasi kueri, indeks, dan konfigurasi, lalu terapkan perubahan yang didukung saat Anda siap.

Lihat [paket saat ini](https://releem.com/pricing) untuk layanan yang dihosting.

<a name="security"></a>

## 🔒 Keamanan

Bergantung pada basis data dan fitur yang diaktifkan, informasi yang dikumpulkan dapat mencakup statistik sumber daya sistem, pengaturan dan status basis data, nama skema dan tabel, struktur dan indeks, statistik eksekusi kueri, contoh kueri, serta rencana EXPLAIN.

Sebelum menghubungkan, tinjau hak akses yang diperlukan untuk basis data dan fitur yang diaktifkan. Baca [FAQ keamanan data](https://releem.com/faq) untuk informasi tentang pengumpulan dan pengiriman data oleh Agent, serta dokumentasi kompatibilitas di bawah untuk persyaratan khusus setiap mesin basis data.

## 🗄️ Kompatibilitas

### Versi basis data

| Basis data | Versi yang didukung dalam dokumentasi |
| --- | --- |
| MySQL | 5.5, 5.6, 5.7, 8.0, 8.4, 9 |
| MariaDB | 10, 11, 12, 13 |
| PostgreSQL | 12, 13, 14, 15, 16, 17, 18 |
| Percona Server for MySQL | 5.7, 8.0, 8.4, 9.7 |

Periksa [basis data, versi, dan izin yang didukung](https://docs.releem.com/supported-databases) untuk persyaratan terkini.

### Lingkungan yang didukung

- **Linux:** CentOS, CloudLinux, Debian, Ubuntu, dan Rocky Linux.
- **Windows Server:** 2012, 2016, 2019, 2022, dan 2025.
- **Kontainer dan orkestrasi:** Docker dan Kubernetes.
- **Amazon RDS:** MySQL, MariaDB, dan PostgreSQL.
- **Amazon Aurora:** MySQL dan PostgreSQL.
- **Google Cloud SQL:** MySQL dan PostgreSQL.
- **Azure Database for MySQL.**
- **Hosting dan klaster:** Klaster MySQL/MariaDB dan WHM/cPanel.

Pilih panduan instalasi untuk [MySQL](https://docs.releem.com/installation/mysql), [MariaDB](https://docs.releem.com/installation/mariadb) atau [PostgreSQL](https://docs.releem.com/installation/postgresql). Pilihan lingkungan, fitur, dan tindakan otomatis bergantung pada basis data, versi, cara penerapan, izin, dan paket.

## 🧪 Pengujian kinerja

Artikel historis ini menjelaskan lingkungan benchmark dan perbandingan konfigurasi tertentu. Hasilnya berlaku untuk kondisi pengujian yang didokumentasikan.

- [Benchmark konfigurasi MySQL 5.7](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [Benchmark konfigurasi MySQL 8](https://releem.com/blog/mysql-8-performance-benchmark)
- [Pengaruh konfigurasi MySQL terhadap aplikasi web](https://releem.com/blog/web-applications-performance)

## 🤝 Dukungan, masukan, dan roadmap

Repositori ini adalah tempat publik untuk masukan, laporan masalah, dan diskusi roadmap seluruh produk Releem, termasuk dasbor, rekomendasi, integrasi, dan Agent.

- **🤝 Dapatkan bantuan:** Bergabunglah dengan [Komunitas Releem di Slack](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ) untuk bertanya, berdiskusi dengan komunitas, dan mendapatkan bantuan penyiapan.
- **🐛 Laporkan bug:** Cari di [Issues](https://github.com/Releem/releem-agent/issues) sebelum melaporkan masalah. Sertakan komponen yang terdampak, versi basis data, cara penerapan, dan langkah untuk mereproduksi masalah.
- **💡 Usulkan perbaikan dan diskusikan prioritas roadmap:** Gunakan [Discussions](https://github.com/Releem/releem-agent/discussions) untuk menjelaskan kasus penggunaan dan hasil yang Anda butuhkan.

Hapus kredensial, informasi pribadi, dan detail sensitif tentang kueri atau skema dari laporan publik.

Ikuti pembaruan produk di [X](https://twitter.com/releemhq) dan [LinkedIn](https://www.linkedin.com/company/releem).

## 🧩 Berkontribusi

Masukan produk dan laporan bug yang dapat direproduksi diterima untuk seluruh Releem. Kontribusi kode di repositori ini berlaku untuk Agent. Jelaskan masalah, perubahan yang diusulkan, dan cara Anda memverifikasinya. Lihat [lisensi](../../LICENSE) repositori untuk ketentuan kode yang dipublikasikan.
