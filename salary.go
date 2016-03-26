// salary
//
// This is a web application for anonymously sharing salary information among a
// pool of participants. Each pool is identified by a UUID. A user cannot view
// the results of the pool unless they first share their information; the pool
// must also meet a mininum number of contributors set at pool creation time.
//
// It requires PostgreSQL. Configure the database connection via the following
// environment variables: SUSER, SPASS, SDB (database name). Commands to
// create the two necessary tables are below.
//
// I meant to clean this up a bit and write tests, but after two months I
// haven't gotten around to it. Sorry for any bad code here.
//
// A good change would be to make hours/wk an enum like the overtime field, so
// that there's less potential for inadvertently knowing who submitted a
// salary ("I know $foo works 42 hours, and this entry is for 42 hours").
//
// Copyright notice:
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/satori/go.uuid"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

// create table pool (
//  pool_id serial primary key,
//  uuid uuid not null,
//  submit uuid not null,
//  name varchar(140) not null,
//  minsize smallint not null
// )

type pool struct {
	Id      int32
	UUID    uuid.UUID
	Submit  uuid.UUID
	Name    string
	MinSize int16
}

// create table salary (
// 	salary_id serial primary key,
// 	amount int not null,
// 	hourswk smallint not null,
// 	overtime varchar(9) check (overtime in ('never', 'rarely', 'sometimes', 'often')) not null,
// 	overtimepaid bool not null,
// 	remote varchar(7) check (remote in ('no', 'special', 'partial', 'yes')) not null,
// 	title varchar (100) not null,
// 	yearsexperience smallint not null,
// 	travel varchar(9) check (travel in ('never', 'rarely', 'sometimes', 'often')) not null,
//  pool_id integer not null,
//  constraint pool_id foreign key (pool_id)
//   references pool (pool_id) match simple
//   on update cascade on delete cascade
// );

type salary struct {
	Amount          int32
	HoursWk         int16
	Overtime        string
	OvertimePaid    bool
	Remote          string
	Title           string
	Travel          string
	YearsExperience int16
}

var (
	db                *sql.DB
	indexPage         []byte
	enterInfoTemplate = template.Must(template.ParseFiles("enter_info.html"))
	poolTemplate      = template.Must(template.ParseFiles("pool.html"))
	notEnoughTemplate = template.Must(template.ParseFiles("notenough.html"))
)

type poolTemplateData struct {
	PoolName string
	Salaries []salary
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Write(indexPage)
}

func submitPool(w http.ResponseWriter, r *http.Request) {
	minSize, err := strconv.ParseUint(r.FormValue("minSize"), 10, 0)
	if err != nil || minSize < 1 {
		log.Println(err)
		http.Error(w, "Invalid minimum share size", http.StatusBadRequest)
		return
	}

	name := r.FormValue("poolName")
	if name == "" {
		log.Println(err)
		http.Error(w, "Invalid pool name", http.StatusBadRequest)
		return
	}

	u := uuid.NewV4().String()
	s := uuid.NewV4().String()

	stmt := "insert into pool(uuid, submit, name, minsize) values($1,$2,$3,$4)"
	if _, err := db.Exec(stmt, u, s, name, minSize); err != nil {
		log.Println(err)
		http.Error(w, "error creating pool; please try again", http.StatusInternalServerError)
		return
	}

	poolUrl := fmt.Sprintf("/pool?id=%s", u)
	http.Redirect(w, r, poolUrl, 303)
}

func poolHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		submitPool(w, r)
		return
	}

	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "missing pool id", http.StatusBadRequest)
		return
	}

	submitted, no_submitted := r.Cookie(fmt.Sprintf("salary_%s", id))

	// Retrieve Pool
	p, err := getPool(id, w)
	if err != nil {
		return
	}

	// Enter salary if no cookie or cookie doesn't match submit key
	if no_submitted != nil || submitted.Value != p.Submit.String() {
		enterSalary(w, r, p)
		return
	}

	// Get count
	var count int16
	stmt := `select count(*) from salary where pool_id=$1`
	err = db.QueryRow(stmt, p.Id).Scan(&count)
	switch {
	case err == sql.ErrNoRows:
		http.Error(w, "requested pool does not exist", http.StatusNotFound)
		return
	case err != nil:
		log.Println(err)
		http.Error(w, "error retrieving pool info", http.StatusInternalServerError)
		return
	}

	if count >= p.MinSize {
		displayPool(w, r, p)
		return
	}

	if err := notEnoughTemplate.Execute(w, p); err != nil {
		log.Println(err.Error())
		http.Error(w, "error rendering template", http.StatusInternalServerError)
		return
	}
	return
}

func getPool(id string, w http.ResponseWriter) (pool, error) {
	gpError := func(e string, status int) (pool, error) {
		log.Println(e)
		http.Error(w, e, status)
		return pool{}, errors.New(e)
	}

	p := pool{}
	var u, s string

	stmt := `select pool_id, uuid, submit, name, minsize from pool where uuid=$1`
	err := db.QueryRow(stmt, id).Scan(&p.Id, &u, &s, &p.Name, &p.MinSize)
	switch {
	case err == sql.ErrNoRows:
		return gpError("requested pool does not exist", http.StatusNotFound)
	case err != nil:
		log.Println(err)
		return gpError("error retrieving pool info", http.StatusInternalServerError)
	}

	p.UUID = uuid.FromStringOrNil(u)
	p.Submit = uuid.FromStringOrNil(s)

	return p, nil
}

func enterSalary(w http.ResponseWriter, r *http.Request, p pool) {
	if err := enterInfoTemplate.Execute(w, p); err != nil {
		log.Println(err.Error())
		http.Error(w, "error rendering template", http.StatusInternalServerError)
		return
	}
}

func submitSalary(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "you must POST a salary; GET not supported", http.StatusNotImplemented)
		return
	}

	isNeg := func(v string) bool {
		n, err := strconv.ParseInt(r.FormValue(v), 10, 32)
		if err != nil {
			// Failure to convert to int can just return true, since we end up
			// erroring out anyways
			return true
		}
		return n < 0
	}

	if isNeg("amount") || isNeg("yearsexperience") || isNeg("hourswk") {
		http.Error(w, "cannot use negative values", http.StatusBadRequest)
		return
	}

	id := r.FormValue("id")
	submitted, err := r.Cookie(fmt.Sprintf("salary_%s", id))

	// For checking if already submitted, don't actually need to compare with
	// the submit key from the pool table
	if submitted.String() != "" {
		http.Error(w, "you have already submitted your salary", http.StatusBadRequest)
		return
	}

	p, err := getPool(id, w)
	if err != nil {
		return
	}

	ins := `insert into salary(amount, hourswk, overtime, overtimepaid, remote, title, yearsexperience, travel, pool_id) values($1,$2,$3,$4,$5,$6,$7,$8,$9)`

	_, err = db.Exec(ins, r.FormValue("amount"), r.FormValue("hourswk"), r.FormValue("overtime"), r.FormValue("overtimepaid") == "paid", r.FormValue("remote"), r.FormValue("title"), r.FormValue("yearsexperience"), r.FormValue("travel"), p.Id)

	if err != nil {
		log.Println(err)
		http.Error(w, "All fields are required. Sorry, no fancy helpful message yet. :)", http.StatusInternalServerError)
		return
	}

	expiration := time.Now().Add(365 * 24 * time.Hour)
	cookie := http.Cookie{Name: fmt.Sprintf("salary_%s", id), Value: p.Submit.String(), Expires: expiration}
	http.SetCookie(w, &cookie)

	poolUrl := fmt.Sprintf("/pool?id=%s", p.UUID.String())
	http.Redirect(w, r, poolUrl, 303)
}

func displayPool(w http.ResponseWriter, r *http.Request, p pool) {
	stmt := `select amount, hourswk, overtimepaid, remote, title, yearsexperience, travel, overtime from salary where pool_id=$1 order by title asc, amount desc`
	rows, err := db.Query(stmt, p.Id)
	switch {
	case err == sql.ErrNoRows:
		http.Error(w, "no salaries for pool", http.StatusNotFound)
		return
	case err != nil:
		log.Println(err)
		http.Error(w, "error retrieving group salary info", http.StatusInternalServerError)
		return
	}

	salaries := make([]salary, 0)

	for rows.Next() {
		s := salary{}
		err = rows.Scan(&s.Amount, &s.HoursWk, &s.OvertimePaid, &s.Remote, &s.Title, &s.YearsExperience, &s.Travel, &s.Overtime)
		if err != nil {
			log.Println(err)
			http.Error(w, "error retrieving individual salary info", http.StatusInternalServerError)
			return
		}
		salaries = append(salaries, s)
	}

	data := poolTemplateData{
		PoolName: p.Name,
		Salaries: salaries,
	}

	if err := poolTemplate.Execute(w, data); err != nil {
		log.Println(err.Error())
		http.Error(w, "error rendering template", http.StatusInternalServerError)
		return
	}
}

func main() {
	ip, err := ioutil.ReadFile("index.html")
	if err != nil {
		panic(err)
	}
	indexPage = ip

	dbinfo := fmt.Sprintf("user=%s password=%s dbname=%s sslmode=disable", os.Getenv("SUSER"), os.Getenv("SPASS"), os.Getenv("SDB"))
	db, err = sql.Open("postgres", dbinfo)
	if err != nil {
		log.Fatal("failed to open database", err)
	}
	defer db.Close()

	http.HandleFunc("/", index)
	http.HandleFunc("/pool", poolHandler)
	http.HandleFunc("/pool/salary", submitSalary)

	log.Fatal(http.ListenAndServe(":9001", nil))
}
