# **Building a Small MPI \+ NFS Cluster for Distributed Image Processing (Debian 12\)**

This document records **every step performed to build, test, and run a small MPI cluster** using three Debian machines.  
The purpose is to allow an AI or automation system to fully understand the setup, configuration, execution workflow, testing procedure, and performance observations.

No unnecessary metadata or names are included.

---

# **Cluster Architecture**

## **Nodes**

Three Debian 12 machines were used.

| Node | Role |
| ----- | ----- |
| master | orchestration, NFS server, participates in computation |
| node1 | worker |
| node2 | worker |

Example IP layout used during experiments:

master  172.16.12.34  
node1   172.16.12.207  
node2   172.16.12.70

All machines used the same username.

---

# **Software Stack**

Components installed across nodes:

SSH  
OpenMPI  
NFS  
Python3  
mpi4py  
numpy  
Pillow  
rsync

Purpose of each:

SSH      → remote execution \+ MPI process launch  
OpenMPI  → MPI runtime  
NFS      → shared filesystem  
Python   → application runtime  
mpi4py   → MPI bindings for Python  
Pillow   → image processing  
rsync    → fast file staging to local disks

---

# **Step 1 — Configure Hostname Resolution**

On **every node**, edit hosts file:

sudo nano /etc/hosts

Add entries:

127.0.0.1 localhost  
172.16.12.34 master  
172.16.12.207 node1  
172.16.12.70 node2

Verify:

ping master  
ping node1  
ping node2

---

# **Step 2 — Passwordless SSH Setup**

On **master** generate SSH key:

ssh-keygen \-t rsa \-b 4096

Accept defaults.

Copy key to workers:

ssh-copy-id user@node1  
ssh-copy-id user@node2

Test:

ssh node1 hostname  
ssh node2 hostname

No password prompt should appear.

---

# **Step 3 — Install OpenMPI**

Run on **all nodes**:

sudo apt update  
sudo apt install \-y build-essential openmpi-bin libopenmpi-dev

Verify:

mpirun \--version  
mpicc \--version

---

# **Step 4 — Configure NFS Shared Storage**

## **Install NFS server on master**

sudo apt install \-y nfs-kernel-server

Create shared directory:

sudo mkdir \-p /srv/cluster\_shared  
sudo chown user:user /srv/cluster\_shared  
sudo chmod 755 /srv/cluster\_shared

Export directory:

echo "/srv/cluster\_shared 172.16.12.0/24(rw,sync,no\_subtree\_check,no\_root\_squash)" \\  
| sudo tee \-a /etc/exports

Apply exports:

sudo exportfs \-a  
sudo systemctl restart nfs-server

Verify exports:

sudo exportfs \-v

---

## **Mount NFS on worker nodes**

On **node1 and node2**:

sudo apt install \-y nfs-common

Create mount point:

sudo mkdir \-p /srv/cluster\_shared

Mount:

sudo mount master:/srv/cluster\_shared /srv/cluster\_shared

Verify:

ls \-l /srv/cluster\_shared

---

## **Persistent mount**

Edit fstab:

sudo nano /etc/fstab

Add:

master:/srv/cluster\_shared /srv/cluster\_shared nfs defaults,noatime 0 0

Test:

sudo umount /srv/cluster\_shared  
sudo mount \-a

---

# **Step 5 — MPI Hostfile**

Create hostfile on master:

cat \> \~/mpi\_hosts \<\< EOF  
master slots=2  
node1 slots=2  
node2 slots=2  
EOF

This allocates 2 MPI slots per node.

---

# **Step 6 — MPI Hello World Test**

Create test program:

cat \> hello.c \<\< EOF  
\#include \<mpi.h\>  
\#include \<stdio.h\>

int main(int argc, char\*\* argv) {  
 MPI\_Init(\&argc, \&argv);

 int rank, size;  
 MPI\_Comm\_rank(MPI\_COMM\_WORLD, \&rank);  
 MPI\_Comm\_size(MPI\_COMM\_WORLD, \&size);

 printf("Hello from rank %d of %d\\n", rank, size);

 MPI\_Finalize();  
}  
EOF

Compile:

mpicc hello.c \-o hello

Copy binary to NFS shared directory:

cp hello /srv/cluster\_shared/

Run cluster test:

env \-u DISPLAY mpirun \--hostfile \~/mpi\_hosts \-np 6 \\  
/srv/cluster\_shared/hello

Expected output:

Hello from rank 0 of 6  
Hello from rank 1 of 6  
...  
Hello from rank 5 of 6

This confirms MPI launches processes across all nodes.

---

# **Step 7 — Project Directory Layout**

Shared directory structure:

/srv/cluster\_shared/  
    hello  
    dist\_image.py  
    input/  
    output\_serial/  
    output\_mpi/  
    output\_mpi\_local/

Local node directories used for faster I/O:

/tmp/img/

/tmp/out\_serial/

/tmp/out\_mpi/

---

# **Step 8 — Python Environment**

Install required packages on **every node**:

sudo apt install \-y python3 \\  
python3-mpi4py \\  
python3-numpy \\  
python3-pil

Verify:

python3 \-c "import mpi4py, PIL, numpy"

---

# **Step 9 — Distributed Image Processing Script**

File:

/srv/cluster\_shared/dist\_image.py

Script:

from mpi4py import MPI  
from PIL import Image, ImageFilter  
import os  
import sys  
import time

comm \= MPI.COMM\_WORLD  
rank \= comm.Get\_rank()  
size \= comm.Get\_size()

if rank \== 0:  
    src\_dir \= sys.argv\[1\]  
    dst\_dir \= sys.argv\[2\]

    os.makedirs(dst\_dir, exist\_ok=True)

    files \= \[  
        f for f in os.listdir(src\_dir)  
        if f.lower().endswith((".png",".jpg",".jpeg"))  
    \]

    start\_time \= time.time()

else:  
    src\_dir \= None  
    dst\_dir \= None  
    files \= None  
    start\_time \= None

src\_dir \= comm.bcast(src\_dir, root=0)  
dst\_dir \= comm.bcast(dst\_dir, root=0)  
files \= comm.bcast(files, root=0)  
start\_time \= comm.bcast(start\_time, root=0)

for f in files\[rank::size\]:

    src \= os.path.join(src\_dir,f)  
    base,ext \= os.path.splitext(f)

    dst \= os.path.join(dst\_dir,base+"\_gray\_blur"+ext)

    img \= Image.open(src).convert("L")  
    img \= img.filter(ImageFilter.GaussianBlur(radius=2))  
    img.save(dst)

comm.Barrier()

if rank \== 0:  
    end \= time.time()  
    print("Elapsed:",end-start\_time)

---

# **Step 10 — Dataset Preparation**

Create input directory:

mkdir \-p /srv/cluster\_shared/input

Copy images into it.

---

# **Step 11 — Stage Images to Local Disks**

Local staging avoids NFS bottlenecks.

On master:

mkdir \-p /tmp/img  
rsync \-av /srv/cluster\_shared/input/ /tmp/img/

On worker nodes:

mkdir \-p /tmp/img  
rsync \-av \--delete master:/srv/cluster\_shared/input/ /tmp/img/

---

# **Step 12 — Serial Baseline Test**

Run on master:

mkdir \-p /tmp/out\_serial

time python3 /srv/cluster\_shared/dist\_image.py \\  
/tmp/img \\  
/tmp/out\_serial

Observed time:

\~8.2 seconds

---

# **Step 13 — MPI Execution**

Prepare output directory on every node:

mkdir \-p /tmp/out\_mpi

Run:

env \-u DISPLAY mpirun \--hostfile \~/mpi\_hosts \-np 6 \\  
python3 /srv/cluster\_shared/dist\_image.py \\  
/tmp/img \\  
/tmp/out\_mpi

Observed runtime:

\~2.2 seconds

---

# **Performance Results**

| Configuration | Processes | I/O | Time |
| ----- | ----- | ----- | ----- |
| Serial | 1 | local disk | \~8.2 s |
| MPI | 6 | NFS | \~60–70 s |
| MPI | 6 | local disk | \~2.2 s |

---

# **Speedup Calculation**

Speedup \= Serial\_Time / Parallel\_Time

Speedup ≈ 8.216 / 2.195  
Speedup ≈ 3.74

---

# **Parallel Efficiency**

Efficiency \= Speedup / Number\_of\_processes

Efficiency ≈ 3.74 / 6  
Efficiency ≈ 0.62

Efficiency ≈ **62%**

---

# **Key Observation**

Running MPI directly on NFS caused **severe slowdown**.

Reason:

many processes  
→ many small reads/writes  
→ network filesystem contention

Solution:

stage data to local disk using rsync  
run MPI on local files

This dramatically improves performance.

---

# **Reusing the Cluster**

## **Run any MPI C program**

Compile:

mpicc program.c \-o program

Copy to shared directory:

cp program /srv/cluster\_shared/

Run:

mpirun \--hostfile \~/mpi\_hosts \-np 6 \\  
/srv/cluster\_shared/program

---

## **Run any Python mpi4py program**

mpirun \--hostfile \~/mpi\_hosts \-np 6 \\  
python3 script.py

---

# **Troubleshooting**

## **Dependency mismatch during install**

Fix:

sudo apt clean  
sudo apt update  
sudo apt full-upgrade

---

## **MPI cannot find executable**

Ensure binary exists on shared filesystem.

---

## **X11 authorization warnings**

Disable DISPLAY:

env \-u DISPLAY mpirun ...

---

## **NFS mount missing**

Mount again:

sudo mount master:/srv/cluster\_shared /srv/cluster\_shared

---

## **Python packages missing on workers**

Install on all nodes:

sudo apt install python3-mpi4py python3-numpy python3-pil

---

## **Slow MPI performance**

Avoid NFS for heavy I/O.

Use:

rsync → local disk → MPI

---

# **End of Document**

